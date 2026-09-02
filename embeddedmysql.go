// Package embeddedmysql runs a real MySQL or MariaDB server for tests and local development.
package embeddedmysql

import (
	"errors"
	"fmt"
	"io"
	"time"

	"github.com/quantumsheep/embedded-mysql/internal/server"
)

type Flavor string

const (
	MySQL   Flavor = "mysql"
	MariaDB Flavor = "mariadb"
)

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

func DefaultConfig() Config {
	return Config{
		flavor:       MySQL,
		username:     "root",
		database:     "test",
		startTimeout: 60 * time.Second,
		logger:       io.Discard,
	}
}

func (c Config) Flavor(flavor Flavor) Config {
	c.flavor = flavor

	return c
}

func (c Config) Version(version string) Config {
	c.version = version

	return c
}

func (c Config) Port(port uint32) Config {
	c.port = port

	return c
}

func (c Config) Username(username string) Config {
	c.username = username

	return c
}

func (c Config) Password(password string) Config {
	c.password = password

	return c
}

func (c Config) Database(database string) Config {
	c.database = database

	return c
}

func (c Config) RuntimePath(path string) Config {
	c.runtimePath = path

	return c
}

func (c Config) CachePath(path string) Config {
	c.cachePath = path

	return c
}

func (c Config) BinaryURL(url string) Config {
	c.binaryURL = url

	return c
}

func (c Config) BasePath(path string) Config {
	c.basePath = path

	return c
}

func (c Config) StartTimeout(timeout time.Duration) Config {
	c.startTimeout = timeout

	return c
}

func (c Config) Logger(writer io.Writer) Config {
	c.logger = writer

	return c
}

type EmbeddedMySQL struct {
	config Config
	server *server.Server
}

func NewDatabase(config ...Config) *EmbeddedMySQL {
	instanceConfig := DefaultConfig()
	if len(config) > 0 {
		instanceConfig = config[0]
	}

	return &EmbeddedMySQL{
		config: instanceConfig,
	}
}

func (m *EmbeddedMySQL) Start() error {
	if m.server != nil {
		return errors.New("embedded-mysql: server is already started")
	}

	startedServer, err := server.Start(server.Options{
		Flavor:       string(m.config.flavor),
		Version:      m.config.version,
		Port:         m.config.port,
		Username:     m.config.username,
		Password:     m.config.password,
		Database:     m.config.database,
		RuntimePath:  m.config.runtimePath,
		CachePath:    m.config.cachePath,
		BinaryURL:    m.config.binaryURL,
		BasePath:     m.config.basePath,
		StartTimeout: m.config.startTimeout,
		Logger:       m.config.logger,
	})
	if err != nil {
		return err
	}

	m.server = startedServer

	return nil
}

func (m *EmbeddedMySQL) Stop() error {
	if m.server == nil {
		return errors.New("embedded-mysql: server is not started")
	}

	err := m.server.Stop()
	m.server = nil

	return err
}

func (m *EmbeddedMySQL) Port() uint32 {
	if m.server == nil {
		return 0
	}

	return m.server.Port()
}

func (m *EmbeddedMySQL) DSN() string {
	return fmt.Sprintf("%s:%s@tcp(127.0.0.1:%d)/%s", m.config.username, m.config.password, m.Port(), m.config.database)
}
