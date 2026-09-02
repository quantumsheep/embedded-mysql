package server

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

type Options struct {
	Flavor       string
	Version      string
	Port         uint32
	Username     string
	Password     string
	Database     string
	RuntimePath  string
	CachePath    string
	BinaryURL    string
	BasePath     string
	StartTimeout time.Duration
	Logger       io.Writer
}

type Server struct {
	options   Options
	port      uint32
	workDir   string
	Cmd       *exec.Cmd
	Watchdog  *watchdog.Watchdog
	waitDone  chan error
	serverLog *bytes.Buffer
}

func Start(options Options) (*Server, error) {
	if options.Logger == nil {
		options.Logger = io.Discard
	}

	flavor := options.Flavor
	if flavor == "" {
		flavor = "mysql"
	}

	serverName := "mysqld"
	if flavor == "mariadb" {
		serverName = "mariadbd"
	}

	base := options.BasePath
	if base == "" && flavor == "mariadb" && runtime.GOOS == "darwin" && options.BinaryURL == "" {
		base = HomebrewMariaDB()
		if base == "" {
			return nil, errors.New(`embedded-mysql: no official MariaDB binaries for macOS; run "brew install mariadb" or set BasePath`)
		}
	}

	installed := base != ""

	var err error

	if base == "" {
		version := options.Version
		if version == "" {
			version = "8.4.6"
			if flavor == "mariadb" {
				version = "11.8.8"
			}
		}

		base, err = download.EnsureBinaries(download.Options{
			Flavor:    flavor,
			Version:   version,
			CachePath: options.CachePath,
			BinaryURL: options.BinaryURL,
			Logger:    options.Logger,
		})
		if err != nil {
			return nil, err
		}
	}

	serverPath := filepath.Join(base, "bin", serverName)
	if runtime.GOOS == "windows" {
		serverPath += ".exe"
	}

	server := &Server{
		options: options,
	}

	// mysqld rejects a socket path longer than 103 characters, so the socket, the pid file and the init file live in a short temporary directory.
	server.workDir, err = os.MkdirTemp("", "emysql-")
	if err != nil {
		return nil, err
	}

	dataDir := filepath.Join(server.workDir, "data")
	pidFilePath := filepath.Join(server.workDir, "mysqld.pid")

	if options.RuntimePath != "" {
		dataDir = filepath.Join(options.RuntimePath, "data")
		pidFilePath = filepath.Join(options.RuntimePath, "mysqld.pid")

		err = os.MkdirAll(dataDir, 0o755)
		if err != nil {
			server.cleanup()

			return nil, err
		}

		// A crashed run can leave a server that holds the data directory and its port. The pid file survives a crash, so it names that server.
		err = stopStaleServer(pidFilePath, options.Logger)
		if err != nil {
			server.cleanup()

			return nil, err
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

		initializeCommand.Stdout = io.MultiWriter(&initializeLog, options.Logger)
		initializeCommand.Stderr = initializeCommand.Stdout

		err = initializeCommand.Run()
		if err != nil {
			server.cleanup()

			return nil, fmt.Errorf("embedded-mysql: initialize failed: %w\n%s", err, initializeLog.String())
		}
	}

	server.port = options.Port

	if server.port == 0 {
		server.port, err = freePort()
		if err != nil {
			server.cleanup()

			return nil, err
		}
	}

	initFilePath := filepath.Join(server.workDir, "init.sql")

	err = os.WriteFile(initFilePath, []byte(initSQL(options)), 0o600)
	if err != nil {
		server.cleanup()

		return nil, err
	}

	arguments := append(commonArguments,
		fmt.Sprintf("--port=%d", server.port),
		"--bind-address=127.0.0.1",
		"--socket="+filepath.Join(server.workDir, "mysql.sock"),
		"--pid-file="+pidFilePath,
		"--init-file="+initFilePath,
	)

	if flavor != "mariadb" {
		// MariaDB knows neither option. Its binary log is off by default.
		arguments = append(arguments, "--mysqlx=OFF", "--disable-log-bin")
	}

	server.serverLog = &bytes.Buffer{}
	server.Cmd = exec.Command(serverPath, arguments...)
	server.Cmd.Stdout = io.MultiWriter(server.serverLog, options.Logger)
	server.Cmd.Stderr = server.Cmd.Stdout

	err = server.Cmd.Start()
	if err != nil {
		server.cleanup()

		return nil, err
	}

	server.waitDone = make(chan error, 1)

	go func() {
		server.waitDone <- server.Cmd.Wait()
	}()

	// The watchdog survives a kill or a crash of the current process and then stops mysqld. Without it that death leaves an orphan server.
	server.Watchdog, err = watchdog.Start(server.Cmd.Process.Pid)
	if err != nil {
		_ = server.Stop()

		return nil, err
	}

	err = server.waitReady()
	if err != nil {
		_ = server.Stop()

		return nil, err
	}

	return server, nil
}

func HomebrewMariaDB() string {
	for _, prefix := range []string{"/opt/homebrew/opt/mariadb", "/usr/local/opt/mariadb"} {
		_, err := os.Stat(filepath.Join(prefix, "bin", "mariadbd"))
		if err == nil {
			return prefix
		}
	}

	return ""
}

// Both forms of mariadb-install-db create root with an empty password, like --initialize-insecure.
func initializeCommand(flavor, serverPath, base, dataDir string, commonArguments []string) *exec.Cmd {
	if flavor != "mariadb" {
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

func initSQL(options Options) string {
	username := strings.ReplaceAll(options.Username, "'", "''")
	password := strings.ReplaceAll(options.Password, "'", "''")
	database := strings.ReplaceAll(options.Database, "`", "``")

	var builder strings.Builder

	fmt.Fprintf(&builder, "CREATE DATABASE IF NOT EXISTS `%s`;\n", database)
	fmt.Fprintf(&builder, "CREATE USER IF NOT EXISTS '%s'@'%%' IDENTIFIED BY '%s';\n", username, password)
	fmt.Fprintf(&builder, "GRANT ALL PRIVILEGES ON *.* TO '%s'@'%%' WITH GRANT OPTION;\n", username)
	builder.WriteString("FLUSH PRIVILEGES;\n")

	return builder.String()
}

func (s *Server) waitReady() error {
	deadline := time.Now().Add(s.options.StartTimeout)
	address := fmt.Sprintf("127.0.0.1:%d", s.port)

	for time.Now().Before(deadline) {
		select {
		case err := <-s.waitDone:
			s.waitDone <- err

			return fmt.Errorf("embedded-mysql: mysqld exited during startup: %v\n%s", err, s.serverLog.String())
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

	return fmt.Errorf("embedded-mysql: server did not accept connections after %s\n%s", s.options.StartTimeout, s.serverLog.String())
}

func (s *Server) Stop() error {
	// The watchdog dies first. A watchdog that outlives this call can send its signal to a recycled pid.
	if s.Watchdog != nil {
		s.Watchdog.Stop()
		s.Watchdog = nil
	}

	_ = proc.Terminate(s.Cmd.Process.Pid)

	select {
	case <-s.waitDone:
	case <-time.After(30 * time.Second):
		_ = s.Cmd.Process.Kill()
		<-s.waitDone
	}

	s.cleanup()

	return nil
}

func (s *Server) cleanup() {
	if s.workDir != "" {
		_ = os.RemoveAll(s.workDir)
		s.workDir = ""
	}
}

func (s *Server) Port() uint32 {
	return s.port
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
