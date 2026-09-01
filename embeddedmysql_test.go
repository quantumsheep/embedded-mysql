package embeddedmysql

import (
	"database/sql"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strconv"
	"sync"
	"testing"
	"time"

	_ "github.com/go-sql-driver/mysql"
	"github.com/quantumsheep/embedded-mysql/internal/proc"
	"github.com/stretchr/testify/require"
)

func startServer(t *testing.T, config Config) (*EmbeddedMySQL, *sql.DB) {
	t.Helper()

	server := NewDatabase(config)

	err := server.Start()
	require.NoError(t, err)

	t.Cleanup(func() {
		if server.cmd != nil {
			require.NoError(t, server.Stop())
		}
	})

	databaseConnection, err := sql.Open("mysql", server.DSN())
	require.NoError(t, err)

	t.Cleanup(func() {
		require.NoError(t, databaseConnection.Close())
	})

	err = databaseConnection.Ping()
	require.NoError(t, err)

	return server, databaseConnection
}

func mustExec(t *testing.T, databaseConnection *sql.DB, query string, arguments ...any) {
	t.Helper()

	_, err := databaseConnection.Exec(query, arguments...)
	require.NoError(t, err, query)
}

func TestServerSuite(t *testing.T) {
	server, databaseConnection := startServer(t, DefaultConfig().Logger(os.Stderr))
	require.NotZero(t, server.Port())

	t.Run("Version", func(t *testing.T) {
		var version string

		err := databaseConnection.QueryRow("SELECT VERSION()").Scan(&version)
		require.NoError(t, err)

		t.Logf("server version: %s", version)
	})

	t.Run("DDLAndDML", func(t *testing.T) {
		mustExec(t, databaseConnection,
			`CREATE TABLE users (
				id INT AUTO_INCREMENT PRIMARY KEY,
				name VARCHAR(255) NOT NULL,
				age INT,
				UNIQUE KEY uq_name (name)
			) ENGINE=InnoDB`)

		result, err := databaseConnection.Exec("INSERT INTO users (name, age) VALUES (?, ?), (?, ?)", "alice", 30, "bob", 25)
		require.NoError(t, err)

		affectedRows, err := result.RowsAffected()
		require.NoError(t, err)
		require.EqualValues(t, 2, affectedRows)

		var age int

		err = databaseConnection.QueryRow("SELECT age FROM users WHERE name = ?", "alice").Scan(&age)
		require.NoError(t, err)
		require.Equal(t, 30, age)

		mustExec(t, databaseConnection, "UPDATE users SET age = 31 WHERE name = 'alice'")
		mustExec(t, databaseConnection, "DELETE FROM users WHERE name = 'bob'")

		var count int

		err = databaseConnection.QueryRow("SELECT COUNT(*) FROM users").Scan(&count)
		require.NoError(t, err)
		require.Equal(t, 1, count)

		_, err = databaseConnection.Exec("INSERT INTO users (name) VALUES ('alice')")
		require.Error(t, err)
	})

	t.Run("Transactions", func(t *testing.T) {
		mustExec(t, databaseConnection, "CREATE TABLE accounts (id INT PRIMARY KEY, balance INT)")
		mustExec(t, databaseConnection, "INSERT INTO accounts VALUES (1, 100), (2, 0)")

		transaction, err := databaseConnection.Begin()
		require.NoError(t, err)

		_, err = transaction.Exec("UPDATE accounts SET balance = balance - 40 WHERE id = 1")
		require.NoError(t, err)

		_, err = transaction.Exec("UPDATE accounts SET balance = balance + 40 WHERE id = 2")
		require.NoError(t, err)

		err = transaction.Commit()
		require.NoError(t, err)

		transaction, err = databaseConnection.Begin()
		require.NoError(t, err)

		_, err = transaction.Exec("UPDATE accounts SET balance = 0 WHERE id = 1")
		require.NoError(t, err)

		err = transaction.Rollback()
		require.NoError(t, err)

		var firstBalance, secondBalance int

		err = databaseConnection.QueryRow("SELECT balance FROM accounts WHERE id = 1").Scan(&firstBalance)
		require.NoError(t, err)

		err = databaseConnection.QueryRow("SELECT balance FROM accounts WHERE id = 2").Scan(&secondBalance)
		require.NoError(t, err)

		require.Equal(t, 60, firstBalance)
		require.Equal(t, 40, secondBalance)
	})

	t.Run("PreparedStatements", func(t *testing.T) {
		statement, err := databaseConnection.Prepare("SELECT ? + ?")
		require.NoError(t, err)

		defer func() {
			require.NoError(t, statement.Close())
		}()

		var sum int

		err = statement.QueryRow(2, 3).Scan(&sum)
		require.NoError(t, err)
		require.Equal(t, 5, sum)
	})

	t.Run("JSONAndJoins", func(t *testing.T) {
		mustExec(t, databaseConnection, "CREATE TABLE docs (id INT PRIMARY KEY, body JSON)")
		mustExec(t, databaseConnection, `INSERT INTO docs VALUES (1, '{"lang": "go", "stars": 5}')`)

		var language string

		err := databaseConnection.QueryRow(`SELECT body->>'$.lang' FROM docs WHERE id = 1`).Scan(&language)
		require.NoError(t, err)
		require.Equal(t, "go", language)

		var name string

		err = databaseConnection.QueryRow(`SELECT u.name FROM users u JOIN accounts a ON a.id = u.id`).Scan(&name)
		require.NoError(t, err)
		require.Equal(t, "alice", name)
	})

	t.Run("Concurrency", func(t *testing.T) {
		mustExec(t, databaseConnection, "CREATE TABLE counters (id INT PRIMARY KEY, n INT)")
		mustExec(t, databaseConnection, "INSERT INTO counters VALUES (1, 0)")

		var waitGroup sync.WaitGroup

		errorsChannel := make(chan error, 10)

		for writer := 0; writer < 10; writer++ {
			waitGroup.Add(1)

			go func() {
				defer waitGroup.Done()

				for update := 0; update < 20; update++ {
					_, err := databaseConnection.Exec("UPDATE counters SET n = n + 1 WHERE id = 1")
					if err != nil {
						errorsChannel <- err

						return
					}
				}
			}()
		}

		waitGroup.Wait()
		close(errorsChannel)

		for err := range errorsChannel {
			require.NoError(t, err)
		}

		var counter int

		err := databaseConnection.QueryRow("SELECT n FROM counters WHERE id = 1").Scan(&counter)
		require.NoError(t, err)
		require.Equal(t, 200, counter)
	})
}

func TestMariaDB(t *testing.T) {
	supported := runtime.GOARCH == "amd64" && (runtime.GOOS == "linux" || runtime.GOOS == "windows")
	if runtime.GOOS == "darwin" && homebrewMariaDB() != "" {
		supported = true
	}

	if !supported {
		t.Skip("no MariaDB binaries for this platform")
	}

	server, databaseConnection := startServer(t, DefaultConfig().Flavor(MariaDB).Logger(os.Stderr))
	require.NotZero(t, server.Port())

	var version string

	err := databaseConnection.QueryRow("SELECT VERSION()").Scan(&version)
	require.NoError(t, err)
	require.Contains(t, version, "MariaDB")

	mustExec(t, databaseConnection, "CREATE TABLE kv (k VARCHAR(64) PRIMARY KEY, v VARCHAR(64))")
	mustExec(t, databaseConnection, "INSERT INTO kv VALUES ('key', 'value')")

	var value string

	err = databaseConnection.QueryRow("SELECT v FROM kv WHERE k = 'key'").Scan(&value)
	require.NoError(t, err)
	require.Equal(t, "value", value)
}

func TestCustomUserAndDatabase(t *testing.T) {
	config := DefaultConfig().Username("app").Password("secret").Database("appdb")
	server, databaseConnection := startServer(t, config)

	expectedDSN := fmt.Sprintf("app:secret@tcp(127.0.0.1:%d)/appdb", server.Port())
	require.Equal(t, expectedDSN, server.DSN())

	var databaseName string

	err := databaseConnection.QueryRow("SELECT DATABASE()").Scan(&databaseName)
	require.NoError(t, err)
	require.Equal(t, "appdb", databaseName)
}

func TestPersistenceAcrossRestart(t *testing.T) {
	runtimePath := filepath.Join(t.TempDir(), "mysql-runtime")
	config := DefaultConfig().RuntimePath(runtimePath)

	server, databaseConnection := startServer(t, config)
	mustExec(t, databaseConnection, "CREATE TABLE kv (k VARCHAR(64) PRIMARY KEY, v VARCHAR(64))")
	mustExec(t, databaseConnection, "INSERT INTO kv VALUES ('key', 'value')")

	require.NoError(t, databaseConnection.Close())
	require.NoError(t, server.Stop())

	_, restartedConnection := startServer(t, config)

	var value string

	err := restartedConnection.QueryRow("SELECT v FROM kv WHERE k = 'key'").Scan(&value)
	require.NoError(t, err)
	require.Equal(t, "value", value)
}

func TestStaleServerStop(t *testing.T) {
	runtimePath := filepath.Join(t.TempDir(), "mysql-runtime")
	config := DefaultConfig().RuntimePath(runtimePath)

	first := NewDatabase(config)

	err := first.Start()
	require.NoError(t, err)

	t.Cleanup(func() {
		_ = first.Stop()
	})

	stalePid := first.cmd.Process.Pid

	// A stopped watchdog and a skipped Stop reproduce a crash of the whole process group.
	first.watchdog.Stop()
	first.watchdog = nil

	_, databaseConnection := startServer(t, config)

	require.False(t, proc.Alive(stalePid), "the stale server still runs")

	err = databaseConnection.Ping()
	require.NoError(t, err)
}

func TestWatchdogStopsOrphan(t *testing.T) {
	runtimePath := filepath.Join(t.TempDir(), "mysql-runtime")

	command := exec.Command(os.Args[0], "-test.run", "^TestHelperStartAndExit$", "-test.v")
	command.Env = append(os.Environ(),
		"EMBEDDED_MYSQL_HELPER=1",
		"EMBEDDED_MYSQL_HELPER_RUNTIME="+runtimePath,
	)

	output, err := command.Output()
	require.NoError(t, err, string(output))

	matches := regexp.MustCompile(`HELPER_MYSQLD_PID=(\d+)`).FindSubmatch(output)
	require.NotNil(t, matches, string(output))

	orphanPid, err := strconv.Atoi(string(matches[1]))
	require.NoError(t, err)

	require.Eventually(t, func() bool {
		if proc.Alive(orphanPid) {
			return false
		}

		return os.RemoveAll(runtimePath) == nil
	}, 30*time.Second, 500*time.Millisecond, "the watchdog did not stop the orphan server")
}

func TestHelperStartAndExit(t *testing.T) {
	if os.Getenv("EMBEDDED_MYSQL_HELPER") != "1" {
		t.Skip("helper for TestWatchdogStopsOrphan")
	}

	config := DefaultConfig().RuntimePath(os.Getenv("EMBEDDED_MYSQL_HELPER_RUNTIME"))
	server := NewDatabase(config)

	err := server.Start()
	require.NoError(t, err)

	fmt.Printf("HELPER_MYSQLD_PID=%d\n", server.cmd.Process.Pid)

	// The exit without Stop leaves the server to the watchdog.
	os.Exit(0)
}

func TestStartStopErrors(t *testing.T) {
	server := NewDatabase()

	err := server.Stop()
	require.Error(t, err)

	err = server.Start()
	require.NoError(t, err)

	err = server.Start()
	require.Error(t, err)

	err = server.Stop()
	require.NoError(t, err)

	err = server.Stop()
	require.Error(t, err)
}
