# embedded-mysql

Run a real MySQL server from Go code, with no installation step. This library is the MySQL equivalent of [embedded-postgres](https://github.com/fergusstrange/embedded-postgres).

The library downloads the official MySQL binaries from `cdn.mysql.com`, caches them in `~/.embedded-mysql`, and starts `mysqld` with an isolated data directory. The first start downloads the binaries. Each later start uses the cache and takes a few seconds.

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

| Setter         | Default             | Effect                                                                                                                         |
| -------------- | ------------------- | ------------------------------------------------------------------------------------------------------------------------------ |
| `Version`      | `8.4.6`             | The MySQL version to download.                                                                                                 |
| `Port`         | `0`                 | The TCP port. Port `0` selects a free port.                                                                                    |
| `Username`     | `root`              | The user that the server creates at start.                                                                                     |
| `Password`     | empty               | The password of the user.                                                                                                      |
| `Database`     | `test`              | The database that the server creates at start.                                                                                 |
| `RuntimePath`  | temporary           | The directory for the data files. A temporary directory is deleted on `Stop`. A set path is kept, and data survives a restart. |
| `CachePath`    | `~/.embedded-mysql` | The directory for the downloaded binaries.                                                                                     |
| `BinaryURL`    | derived             | A full URL to a MySQL tarball, for platforms that the default URL scheme does not cover.                                       |
| `StartTimeout` | `60s`               | The maximum wait for the server to accept connections.                                                                         |
| `Logger`       | discard             | A writer that receives the `mysqld` output.                                                                                    |

`server.DSN()` returns a data source name for `github.com/go-sql-driver/mysql`.
`server.Port()` returns the selected port.

## Supported platforms

| Platform                   | Source archive                                        |
| -------------------------- | ----------------------------------------------------- |
| macOS arm64 / x86_64       | `mysql-VERSION-macos15-ARCH.tar.gz`                   |
| Linux x86_64 (glibc 2.28+) | `mysql-VERSION-linux-glibc2.28-x86_64-minimal.tar.xz` |
| Linux arm64 (glibc 2.28+)  | `mysql-VERSION-linux-glibc2.28-aarch64.tar.xz`        |
| Windows x86_64             | `mysql-VERSION-winx64.zip`                            |

## Tests

```sh
go test ./...
```
