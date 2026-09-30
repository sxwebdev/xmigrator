# xmigrator

A SQL migration library for Go 1.27+ with PostgreSQL (pgx v5) and SQLite support. The core has no external dependencies; database drivers, CLI integrations, and the standalone command are separate Go modules.

- Apply or roll back migrations with a step limit.
- Apply newly added migrations even when their timestamp precedes already applied migrations.
- Run each migration and its typed Go hooks in one transaction, with locking across the entire run.
- Create migration files, inspect status, validate sources, drop application objects, and repair down metadata.
- Configure checksum verification and plug in your own logger.

## Packages

| Module | Purpose |
| --- | --- |
| `github.com/sxwebdev/xmigrator` | Migration runner, sources, hooks, and logging |
| `github.com/sxwebdev/xmigrator/driver/pgx` | PostgreSQL driver using native pgx v5 |
| `github.com/sxwebdev/xmigrator/driver/sqlite` | SQLite driver using your existing `*sql.DB`; your application chooses the SQLite engine |
| `github.com/sxwebdev/xmigrator/cli/urfavecli` | Ready-to-use urfave/cli v3 commands for your application |
| `github.com/sxwebdev/xmigrator/cmd/xmigrator` | Standalone CLI with PostgreSQL and SQLite engines included |

## Library usage

Load migrations from any `fs.FS`, including embedded files, and use your application's connection pool:

```go
package database

import (
    "context"
    "embed"

    "github.com/jackc/pgx/v5/pgxpool"
    "github.com/sxwebdev/xmigrator"
    pgdriver "github.com/sxwebdev/xmigrator/driver/pgx"
)

//go:embed migrations/*.sql
var files embed.FS

func Migrate(ctx context.Context, pool *pgxpool.Pool) error {
    source, err := xmigrator.NewSource(files, "migrations")
    if err != nil {
        return err
    }
    driver, err := pgdriver.FromPool(pool, pgdriver.Config{})
    if err != nil {
        return err
    }
    runner, err := xmigrator.New(source, driver)
    if err != nil {
        return err
    }
    _, err = runner.Up(ctx, 0) // Apply all pending migrations.
    return err
}
```

For SQLite, create the driver with `sqlite.FromDB(db, sqlite.Config{})` from `github.com/sxwebdev/xmigrator/driver/sqlite` and pass it to the same `xmigrator.New` constructor. Importing this driver does not bring a SQLite engine into your application.

```go
_, err = runner.Up(ctx, 2)   // Apply up to two pending migrations.
_, err = runner.Down(ctx, 1) // Roll back the last applied migration.
_, err = runner.DownAll(ctx) // Roll back all applied migrations.
status, err := runner.Status(ctx)
```

Checksum verification defaults to strict and can be changed through `WithChecksumPolicy[T]`. Register transaction hooks with `WithHooks[T]` and a logger with `WithLogger[T]`; `NewSlogLogger` adapts the standard library logger.

See the [usage guide](docs/usage.md) for migration files, hooks, configuration, and embedding commands into an urfave/cli v3 application.

## Standalone CLI

Install the standalone command:

```sh
go install github.com/sxwebdev/xmigrator/cmd/xmigrator@latest
```

To build from this checkout:

```sh
go run tools/dev.go workspace
make build
```

Create migrations, edit the generated SQL files, then apply them. For a local build, use `./build/xmigrator` in place of `xmigrator`:

```sh
mkdir -p migrations
xmigrator create --path ./migrations --name create_users
xmigrator validate --driver sqlite --path ./migrations
xmigrator up --driver sqlite --dsn ./app.db --path ./migrations
xmigrator down --driver sqlite --dsn ./app.db --path ./migrations --steps 1
```

For PostgreSQL, use `--driver pgx` and a PostgreSQL connection string through `--dsn` or `XMIGRATOR_DSN`. Run `xmigrator --help` or `xmigrator <command> --help` for all options. Commands: `up`, `down`, `drop`, `create`, `status`, `validate`, and `repair-down`.

## Roadmap

- MariaDB driver.
- ClickHouse driver.
- Additional CLI framework adapters.

See the [documentation](docs/README.md) for drivers, migration files, hooks, CLI integration, and operations.

Changes are tracked in the [changelog](CHANGELOG.md). Licensed under [MIT](LICENSE).
