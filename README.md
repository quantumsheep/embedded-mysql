# embedded-mysql

Run a real MySQL or MariaDB server from Go code, with no installation step. This library is the MySQL equivalent of [embedded-postgres](https://github.com/fergusstrange/embedded-postgres).

The library downloads the official binaries, caches them in `~/.embedded-mysql`, and starts the server with an isolated data directory. The first start downloads the binaries. Each later start uses the cache and takes a few seconds.

## Usage

```go
package main

import (
	"database/sql"
	"log"

	embeddedmysql "github.com/quantumsheep/embedded-mysql"
	_ "github.com/go-sql-driver/mysql"
)

func main() {
	server := embeddedmysql.NewDatabase()
	if err := server.Start(); err != nil {
		log.Fatal(err)
	}
	defer server.Stop()

	db, err := sql.Open("mysql", server.DSN())
	if err != nil {
		log.Fatal(err)
	}
	defer db.Close()
	// Use db as a normal MySQL connection.
}
```

## Configuration

Build a configuration with `DefaultConfig()` and the fluent setters:

```go
server := embeddedmysql.NewDatabase(embeddedmysql.DefaultConfig().
	Version("9.4.0").
	Port(3310).
	Username("app").
	Password("secret").
	Database("appdb").
	RuntimePath("/tmp/my-data").
	StartTimeout(90 * time.Second).
	Logger(os.Stderr))
```

| Setter         | Default             | Effect                                                                                                                             |
| -------------- | ------------------- | ---------------------------------------------------------------------------------------------------------------------------------- |
| `Flavor`       | `MySQL`             | The server implementation, `MySQL` or `MariaDB`.                                                                                   |
| `Version`      | per flavor          | The server version to download. MySQL defaults to `8.4.6`, MariaDB to `11.8.8`.                                                    |
| `Port`         | `0`                 | The TCP port. Port `0` selects a free port.                                                                                        |
| `Username`     | `root`              | The user that the server creates at start.                                                                                         |
| `Password`     | empty               | The password of the user.                                                                                                          |
| `Database`     | `test`              | The database that the server creates at start.                                                                                     |
| `RuntimePath`  | temporary           | The directory for the data files. A temporary directory is deleted on `Stop`. A set path is kept, and data survives a restart.     |
| `CachePath`    | `~/.embedded-mysql` | The directory for the downloaded binaries.                                                                                         |
| `BinaryURL`    | derived             | A full URL to a MySQL tarball, for platforms that the default URL scheme does not cover.                                           |
| `BasePath`     | empty               | The directory of an installed server, the one that contains `bin/mysqld` or `bin/mariadbd`. Skips the download, ignores `Version`. |
| `StartTimeout` | `60s`               | The maximum wait for the server to accept connections.                                                                             |
| `Logger`       | discard             | A writer that receives the `mysqld` output.                                                                                        |

`server.DSN()` returns a data source name for `github.com/go-sql-driver/mysql`.
`server.Port()` returns the selected port.

## MariaDB

```go
server := embeddedmysql.NewDatabase(embeddedmysql.DefaultConfig().
	Flavor(embeddedmysql.MariaDB))
```

`server.DSN()` works unchanged: `github.com/go-sql-driver/mysql` speaks to MariaDB.

MariaDB publishes no standalone macOS binaries, so on macOS the library uses a [Homebrew](https://formulae.brew.sh/formula/mariadb) installation: run `brew install mariadb` once, and the library finds it automatically. `BasePath` points to any other installation.

## Supported platforms

| Platform                   | MySQL source archive                                  | MariaDB source archive                        |
| -------------------------- | ----------------------------------------------------- | --------------------------------------------- |
| macOS arm64 / x86_64       | `mysql-VERSION-macos15-ARCH.tar.gz`                   | Homebrew `mariadb`, found automatically       |
| Linux x86_64 (glibc 2.28+) | `mysql-VERSION-linux-glibc2.28-x86_64-minimal.tar.xz` | `mariadb-VERSION-linux-systemd-x86_64.tar.gz` |
| Linux arm64 (glibc 2.28+)  | `mysql-VERSION-linux-glibc2.28-aarch64.tar.xz`        | none published, set `BinaryURL`               |
| Windows x86_64             | `mysql-VERSION-winx64.zip`                            | `mariadb-VERSION-winx64.zip`                  |

## Tests

```sh
go test ./...
```
