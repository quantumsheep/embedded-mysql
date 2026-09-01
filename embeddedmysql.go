// Package embeddedmysql runs a real MySQL or MariaDB server for tests and local development. It downloads the official binaries once, caches them, and starts the server with an isolated data directory.
package embeddedmysql

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"

	"github.com/quantumsheep/embedded-mysql/internal/download"
	"github.com/quantumsheep/embedded-mysql/internal/proc"
	"github.com/quantumsheep/embedded-mysql/internal/watchdog"
)

// Flavor selects the server implementation.
type Flavor string

const (
	// MySQL runs the official MySQL server binaries.
	MySQL Flavor = "mysql"
	// MariaDB runs the official MariaDB server binaries.
	MariaDB Flavor = "mariadb"
)

// Config holds the settings of an embedded server. Build it with DefaultConfig and change it with the fluent setters.
type Config struct {
	flavor       Flavor
	version      string
	port         uint32
	username     string
	password     string
	database     string
	runtimePath  string
	cachePath    string
	binaryURL    string
	basePath     string
	startTimeout time.Duration
	logger       io.Writer
}

// DefaultConfig returns the default settings: MySQL in its default version, user "root" with no password, database "test", and an automatic free port.
func DefaultConfig() Config {
	return Config{
		flavor:       MySQL,
		username:     "root",
		database:     "test",
		startTimeout: 60 * time.Second,
		logger:       io.Discard,
	}
}

// Flavor sets the server implementation, MySQL or MariaDB. The default is MySQL.
func (c Config) Flavor(flavor Flavor) Config {
	c.flavor = flavor

	return c
}

// Version sets the server version to download, for example "8.4.6" for MySQL or "11.8.8" for MariaDB. When not set, the flavor default applies: MySQL 8.4.6, MariaDB 11.8.8.
func (c Config) Version(version string) Config {
	c.version = version

	return c
}

// Port sets the TCP port. Port 0 selects a free port automatically.
func (c Config) Port(port uint32) Config {
	c.port = port

	return c
}

// Username sets the user that the server creates at start.
func (c Config) Username(username string) Config {
	c.username = username

	return c
}

// Password sets the password of the user.
func (c Config) Password(password string) Config {
	c.password = password

	return c
}

// Database sets the database that the server creates at start.
func (c Config) Database(database string) Config {
	c.database = database

	return c
}

// RuntimePath sets a persistent directory for the data files. When not set, the server uses a temporary directory and deletes it on Stop.
func (c Config) RuntimePath(path string) Config {
	c.runtimePath = path

	return c
}

// CachePath sets the directory for the downloaded binaries. The default is ~/.embedded-mysql.
func (c Config) CachePath(path string) Config {
	c.cachePath = path

	return c
}

// BinaryURL sets a full URL to a MySQL binary tarball. Use it for a version or platform that the default URL scheme does not cover.
func (c Config) BinaryURL(url string) Config {
	c.binaryURL = url

	return c
}

// BasePath sets the directory of an installed server, the one that contains bin/mysqld or bin/mariadbd, for example "/opt/homebrew/opt/mariadb". The library then skips the download, and Version has no effect.
func (c Config) BasePath(path string) Config {
	c.basePath = path

	return c
}

// StartTimeout sets the maximum wait for the server to accept connections.
func (c Config) StartTimeout(timeout time.Duration) Config {
	c.startTimeout = timeout

	return c
}

// Logger sets a writer that receives the mysqld output and progress messages.
func (c Config) Logger(writer io.Writer) Config {
	c.logger = writer

	return c
}

// EmbeddedMySQL is one embedded server instance.
type EmbeddedMySQL struct {
	config    Config
	port      uint32
	workDir   string
	cmd       *exec.Cmd
	watchdog  *watchdog.Watchdog
	waitDone  chan error
	serverLog *bytes.Buffer
}

// NewDatabase creates a server instance. Pass no argument for the default configuration, or pass one Config.
func NewDatabase(config ...Config) *EmbeddedMySQL {
	instanceConfig := DefaultConfig()
	if len(config) > 0 {
		instanceConfig = config[0]
	}

	return &EmbeddedMySQL{
		config: instanceConfig,
	}
}

// Start downloads the binaries when necessary, initializes the data directory when necessary, and starts mysqld. Start returns when the server accepts TCP connections.
func (m *EmbeddedMySQL) Start() error {
	if m.cmd != nil {
		return errors.New("embedded-mysql: server is already started")
	}

	if m.config.logger == nil {
		m.config.logger = io.Discard
	}

	flavor := m.config.flavor
	if flavor == "" {
		flavor = MySQL
	}

	serverName := "mysqld"
	if flavor == MariaDB {
		serverName = "mariadbd"
	}

	// An installed server keeps its own layout and needs no download.
	base := m.config.basePath
	if base == "" && flavor == MariaDB && runtime.GOOS == "darwin" && m.config.binaryURL == "" {
		// MariaDB publishes no official macOS binaries, so the Homebrew installation fills the gap.
		base = homebrewMariaDB()
		if base == "" {
			return errors.New(`embedded-mysql: no official MariaDB binaries for macOS; run "brew install mariadb" or set BasePath`)
		}
	}

	installed := base != ""

	var err error

	if base == "" {
		version := m.config.version
		if version == "" {
			version = "8.4.6"
			if flavor == MariaDB {
				version = "11.8.8"
			}
		}

		base, err = download.EnsureBinaries(download.Options{
			Flavor:    string(flavor),
			Version:   version,
			CachePath: m.config.cachePath,
			BinaryURL: m.config.binaryURL,
			Logger:    m.config.logger,
		})
		if err != nil {
			return err
		}
	}

	serverPath := filepath.Join(base, "bin", serverName)
	if runtime.GOOS == "windows" {
		serverPath += ".exe"
	}

	// mysqld rejects a socket path longer than 103 characters, so the socket, the pid file and the init file live in a short temporary directory.
	m.workDir, err = os.MkdirTemp("", "emysql-")
	if err != nil {
		return err
	}

	dataDir := filepath.Join(m.workDir, "data")
	pidFilePath := filepath.Join(m.workDir, "mysqld.pid")

	if m.config.runtimePath != "" {
		dataDir = filepath.Join(m.config.runtimePath, "data")
		pidFilePath = filepath.Join(m.config.runtimePath, "mysqld.pid")

		err = os.MkdirAll(dataDir, 0o755)
		if err != nil {
			m.cleanup()

			return err
		}

		// A crashed run can leave a server that holds the data directory and its port. The pid file survives a crash, so it names that server.
		err = stopStaleServer(pidFilePath, m.config.logger)
		if err != nil {
			m.cleanup()

			return err
		}
	}

	commonArguments := []string{
		"--no-defaults",
		"--basedir=" + base,
		"--datadir=" + dataDir,
	}

	// The extracted tarballs need the override because their compiled-in paths point elsewhere. An installed server has correct compiled-in paths, and its layout varies.
	if !installed {
		commonArguments = append(commonArguments, "--lc-messages-dir="+filepath.Join(base, "share"))
	}

	// mysqld and mariadbd refuse to run as root without an explicit --user, and containers often run as root.
	if runtime.GOOS != "windows" && os.Geteuid() == 0 {
		commonArguments = append(commonArguments, "--user=root")
	}

	_, err = os.Stat(filepath.Join(dataDir, "mysql"))
	if err != nil {
		initializeCommand := initializeCommand(flavor, serverPath, base, dataDir, commonArguments)

		var initializeLog bytes.Buffer

		initializeCommand.Stdout = io.MultiWriter(&initializeLog, m.config.logger)
		initializeCommand.Stderr = initializeCommand.Stdout

		err = initializeCommand.Run()
		if err != nil {
			m.cleanup()

			return fmt.Errorf("embedded-mysql: initialize failed: %w\n%s", err, initializeLog.String())
		}
	}

	m.port = m.config.port

	if m.port == 0 {
		m.port, err = freePort()
		if err != nil {
			m.cleanup()

			return err
		}
	}

	initFilePath := filepath.Join(m.workDir, "init.sql")

	err = os.WriteFile(initFilePath, []byte(m.initSQL()), 0o600)
	if err != nil {
		m.cleanup()

		return err
	}

	arguments := append(commonArguments,
		fmt.Sprintf("--port=%d", m.port),
		"--bind-address=127.0.0.1",
		"--socket="+filepath.Join(m.workDir, "mysql.sock"),
		"--pid-file="+pidFilePath,
		"--init-file="+initFilePath,
	)

	if flavor == MySQL {
		// MariaDB knows neither option. Its binary log is off by default.
		arguments = append(arguments, "--mysqlx=OFF", "--disable-log-bin")
	}

	m.serverLog = &bytes.Buffer{}
	m.cmd = exec.Command(serverPath, arguments...)
	m.cmd.Stdout = io.MultiWriter(m.serverLog, m.config.logger)
	m.cmd.Stderr = m.cmd.Stdout

	err = m.cmd.Start()
	if err != nil {
		m.cmd = nil
		m.cleanup()

		return err
	}

	m.waitDone = make(chan error, 1)

	go func() {
		m.waitDone <- m.cmd.Wait()
	}()

	// The watchdog survives a kill or a crash of the current process and then stops mysqld. Without it that death leaves an orphan server.
	m.watchdog, err = watchdog.Start(m.cmd.Process.Pid)
	if err != nil {
		_ = m.Stop()

		return err
	}

	err = m.waitReady()
	if err != nil {
		_ = m.Stop()

		return err
	}

	return nil
}

// homebrewMariaDB returns the base directory of a Homebrew MariaDB installation, or an empty string.
func homebrewMariaDB() string {
	for _, prefix := range []string{"/opt/homebrew/opt/mariadb", "/usr/local/opt/mariadb"} {
		_, err := os.Stat(filepath.Join(prefix, "bin", "mariadbd"))
		if err == nil {
			return prefix
		}
	}

	return ""
}

// initializeCommand builds the command that creates the system tables in an empty data directory. MySQL has a mysqld mode for that. MariaDB has a separate install program: a script on Unix, an executable on Windows. Both MariaDB forms create root with an empty password, like --initialize-insecure.
func initializeCommand(flavor Flavor, serverPath, base, dataDir string, commonArguments []string) *exec.Cmd {
	if flavor != MariaDB {
		return exec.Command(serverPath, append(commonArguments, "--initialize-insecure")...)
	}

	if runtime.GOOS == "windows" {
		return exec.Command(filepath.Join(base, "bin", "mariadb-install-db.exe"), "--datadir="+dataDir)
	}

	// The tarballs ship the install script in scripts/. Installed servers, Homebrew for example, ship it in bin/.
	scriptPath := filepath.Join(base, "scripts", "mariadb-install-db")

	_, err := os.Stat(scriptPath)
	if err != nil {
		scriptPath = filepath.Join(base, "bin", "mariadb-install-db")
	}

	return exec.Command(scriptPath,
		"--no-defaults",
		"--basedir="+base,
		"--datadir="+dataDir,
		"--auth-root-authentication-method=normal",
		"--skip-test-db",
	)
}

func (m *EmbeddedMySQL) initSQL() string {
	username := strings.ReplaceAll(m.config.username, "'", "''")
	password := strings.ReplaceAll(m.config.password, "'", "''")
	database := strings.ReplaceAll(m.config.database, "`", "``")

	var builder strings.Builder

	fmt.Fprintf(&builder, "CREATE DATABASE IF NOT EXISTS `%s`;\n", database)
	fmt.Fprintf(&builder, "CREATE USER IF NOT EXISTS '%s'@'%%' IDENTIFIED BY '%s';\n", username, password)
	fmt.Fprintf(&builder, "GRANT ALL PRIVILEGES ON *.* TO '%s'@'%%' WITH GRANT OPTION;\n", username)
	builder.WriteString("FLUSH PRIVILEGES;\n")

	return builder.String()
}

func (m *EmbeddedMySQL) waitReady() error {
	deadline := time.Now().Add(m.config.startTimeout)
	address := fmt.Sprintf("127.0.0.1:%d", m.port)

	for time.Now().Before(deadline) {
		select {
		case err := <-m.waitDone:
			m.waitDone <- err

			return fmt.Errorf("embedded-mysql: mysqld exited during startup: %v\n%s", err, m.serverLog.String())
		default:
		}

		connection, err := net.DialTimeout("tcp", address, time.Second)
		if err == nil {
			// A read of one byte of the server greeting proves that mysqld, not another process, owns the port.
			_ = connection.SetReadDeadline(time.Now().Add(2 * time.Second))
			greeting := make([]byte, 1)
			_, err = connection.Read(greeting)
			_ = connection.Close()

			if err == nil {
				return nil
			}
		}

		time.Sleep(100 * time.Millisecond)
	}

	return fmt.Errorf("embedded-mysql: server did not accept connections after %s\n%s", m.config.startTimeout, m.serverLog.String())
}

// Stop shuts the server down and waits for the process to exit. When the configuration has no RuntimePath, Stop also deletes the data directory.
func (m *EmbeddedMySQL) Stop() error {
	if m.cmd == nil {
		return errors.New("embedded-mysql: server is not started")
	}

	// The watchdog dies first. A watchdog that outlives this call can send its signal to a recycled pid.
	if m.watchdog != nil {
		m.watchdog.Stop()
		m.watchdog = nil
	}

	_ = proc.Terminate(m.cmd.Process.Pid)

	select {
	case <-m.waitDone:
	case <-time.After(30 * time.Second):
		_ = m.cmd.Process.Kill()
		<-m.waitDone
	}

	m.cmd = nil
	m.cleanup()

	return nil
}

func (m *EmbeddedMySQL) cleanup() {
	if m.workDir != "" {
		_ = os.RemoveAll(m.workDir)
		m.workDir = ""
	}
}

// Port returns the TCP port of the running server.
func (m *EmbeddedMySQL) Port() uint32 {
	return m.port
}

// DSN returns a data source name for github.com/go-sql-driver/mysql.
func (m *EmbeddedMySQL) DSN() string {
	return fmt.Sprintf("%s:%s@tcp(127.0.0.1:%d)/%s", m.config.username, m.config.password, m.port, m.config.database)
}

func stopStaleServer(pidFilePath string, logger io.Writer) error {
	content, err := os.ReadFile(pidFilePath)
	if err != nil {
		return nil
	}

	pid, err := strconv.Atoi(strings.TrimSpace(string(content)))
	if err != nil {
		return nil
	}

	// A pid file that a crash left behind can name a recycled pid. The process name check keeps the signal away from an unrelated process.
	if !proc.IsServer(pid) {
		return nil
	}

	fmt.Fprintf(logger, "embedded-mysql: stopping stale server with pid %d\n", pid)
	_ = proc.Terminate(pid)

	deadline := time.Now().Add(30 * time.Second)

	for time.Now().Before(deadline) {
		if !proc.Alive(pid) {
			return nil
		}

		time.Sleep(100 * time.Millisecond)
	}

	_ = proc.Kill(pid)
	deadline = time.Now().Add(5 * time.Second)

	for time.Now().Before(deadline) {
		if !proc.Alive(pid) {
			return nil
		}

		time.Sleep(100 * time.Millisecond)
	}

	return fmt.Errorf("embedded-mysql: stale server with pid %d did not stop", pid)
}

func freePort() (uint32, error) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return 0, err
	}

	defer func() { _ = listener.Close() }()

	return uint32(listener.Addr().(*net.TCPAddr).Port), nil
}
