# Library usage

## Installation

Install the core and your chosen driver. The CLI and other drivers are not added to your application automatically:

```sh
go get github.com/sxwebdev/xmigrator
go get github.com/sxwebdev/xmigrator/driver/pgx
# Or select SQLite:
go get github.com/sxwebdev/xmigrator/driver/sqlite
```

The core has no external dependencies. The SQLite driver accepts an existing `*sql.DB` without importing an engine; the application chooses its engine. The standalone command includes modernc SQLite.

## PostgreSQL

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
var migrations embed.FS

func Migrate(ctx context.Context, pool *pgxpool.Pool) error {
    source, err := xmigrator.NewSource(migrations, "migrations")
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
    _, err = runner.Up(ctx, 0)
    return err
}
```

The SQL file pair for this example is in the [migration guide](migrations.md). The application retains ownership of the pool.

## Sources

`NewSource(files fs.FS, dir string, options ...SourceOption)` accepts `embed.FS`, `os.DirFS`, or another `fs.FS` implementation. `dir` is a path within the filesystem; use `"."` for its root. Only that directory is read, without recursive traversal. An existing empty directory is valid; a missing directory returns an error when read.

Source, Migrator, and built-in driver constructors do not connect to the database. The source is read again for each operation, and its SQL is captured in a snapshot for that run.

A source has no explicitly selected dialect by default. `Snapshot` and `Validate` fall back to pgx when none is selected. A Migrator using a built-in driver infers the dialect from that driver. Select the dialect explicitly for standalone SQLite validation:

```go
source, err := xmigrator.NewSource(os.DirFS("./migrations"), ".",
    xmigrator.WithDialect("sqlite"),
)
if err != nil {
    return err
}
report, err := xmigrator.Validate(ctx, source)
```

`Validate` checks file pairs, directives, and transaction ownership without a database. It does not check SQL against a server, verify object existence, or require registered Go hooks.

## Operations

| Call | Behavior |
| --- | --- |
| `Up(ctx, 0)` | Apply all pending migrations |
| `Up(ctx, n)` | Apply up to n pending migrations, n > 0 |
| `Down(ctx, n)` | Revert up to n most recently applied migrations, n > 0 |
| `DownAll(ctx)` | Revert all applied migrations |
| `Status(ctx)` | Read-only inspection of the source and history |
| `Drop(ctx)` | Remove application objects within the driver's scope |
| `PlanRepairDown(ctx, version)` | Preview a down metadata change without writing |
| `RepairDown(ctx, version)` | Accept the current down definition without executing it |
| `Create(ctx, options)` | Create a migration file pair on disk without a database |

Pending migrations are the set difference between the source and history, rather than versions above the maximum applied version. Up applies pending migrations in ascending version order: a newly added migration 200 is applied even if 300 has already been applied. Down follows the reverse order of actual application, rather than version order.

Each migration runs in its own transaction with its hooks and history update. A lock covers the entire run. If the third step fails, the first two confirmed commits remain in the database and in `Result.Actions`; the entire run is not a single transaction.

## Policies and logging

```go
runner, err := xmigrator.New(source, driver,
    xmigrator.WithChecksumPolicy[pgx.Tx](xmigrator.ChecksumWarn),
    xmigrator.WithUnknownAppliedPolicy[pgx.Tx](xmigrator.UnknownAppliedError),
    xmigrator.WithLogger[pgx.Tx](xmigrator.NewSlogLogger(slog.Default())),
)
```

In this snippet, `pgx` is `github.com/jackc/pgx/v5` and `slog` is `log/slog`. For SQLite, the option type parameter is `sqlite.Tx` from the driver package.

Checksum verification is optional: `ChecksumStrict`, the default, rejects mismatches; `ChecksumWarn` allows them and reports issues; `ChecksumDisabled` disables SQL hash comparisons. SHA-256 hashes are still stored when a migration is applied. The policy does not disable name, down kind, or hook revision checks, and does not rewrite history.

`UnknownAppliedAllow`, the default, allows Up to proceed when applied versions are absent from the source, with diagnostic issues. `UnknownAppliedError` rejects such an Up. Down always requires a current file for each selected version: the allow policy cannot recover missing SQL.

No logs are emitted without a logger. Supply an interface implementing `Debugw`, `Infow`, `Warnw`, and `Errorw`, each with the signature `(string, ...any)`. `NewSlogLogger` adapts the standard library's slog logger.

Pass a context with a deadline to bound execution time. See [drivers](drivers.md) for backend settings and [operations](operations.md) for handling partial results.
