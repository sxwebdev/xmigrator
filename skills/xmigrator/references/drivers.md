# Database drivers

## PostgreSQL / pgx v5

Import native pgx as `github.com/jackc/pgx/v5` and the driver as `pgdriver "github.com/sxwebdev/xmigrator/driver/pgx"` to avoid name collisions.

```go
package database

import (
    "context"
    "io/fs"

    "github.com/jackc/pgx/v5/pgxpool"
    "github.com/sxwebdev/xmigrator"
    pgdriver "github.com/sxwebdev/xmigrator/driver/pgx"
)

func Migrate(ctx context.Context, files fs.FS, pool *pgxpool.Pool) error {
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
    _, err = runner.Up(ctx, 0)
    return err
}
```

Alternative constructors are `FromConfig(*pgx.ConnConfig, Config)` and `FromDSN(string, Config)`. Parse configs through pgx; constructors do not connect. FromPool preserves the application's custom dialer, hijacks one connection per run, and physically closes that connection afterward. The pool remains owned by the application. Hooks receive `pgx.Tx`.

Config fields: `MetadataSchema`, `MetadataPrefix`, `RequireExistingMetadata`, `DropSchemas`, `LockWaitTimeout`, `DDLLockTimeout`, `StatementTimeout`. DDL and statement timeouts must be whole milliseconds. Zero DDL/statement timeouts preserve inherited backend settings. Context deadlines remain the caller's overall bound. Defaults place `__xmigrator_history` and `__xmigrator_meta` in schema `xmigrator`.

Use direct connections or session pooling; transaction/statement pooling cannot preserve the session advisory lock. Supported PostgreSQL baseline is 14+.

For a restricted application role, provision metadata with an administrative driver via `WithSession` and `session.EnsureMetadata(ctx)`, grant the required schema/table/identity-sequence privileges, then set `RequireExistingMetadata: true`. Metadata conflicts are errors, not invitations to reuse arbitrary similarly named tables.

Drop requires explicit `DropSchemas`. It retains the schemas and their ownership/grants, removes supported application objects, and preserves metadata. Metadata/system schemas are invalid scope. External dependencies, extension-owned objects, and unsupported object classes reject the operation atomically. Do not derive drop scope from migration filenames.

## SQLite / database/sql

Import `sqdriver "github.com/sxwebdev/xmigrator/driver/sqlite"`. Supply an existing `*sql.DB` with an engine already registered by the application; the driver imports no SQLite engine.

```go
package database

import (
    "context"
    "database/sql"
    "io/fs"

    "github.com/sxwebdev/xmigrator"
    sqdriver "github.com/sxwebdev/xmigrator/driver/sqlite"
)

func Migrate(ctx context.Context, files fs.FS, db *sql.DB) error {
    source, err := xmigrator.NewSource(files, "migrations")
    if err != nil {
        return err
    }
    driver, err := sqdriver.FromDB(db, sqdriver.Config{})
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

Config fields: `MetadataPrefix`, `BusyTimeout`, `LockWaitTimeout`, `Memory`, `LockIdentity`. BusyTimeout uses whole milliseconds; zero preserves the borrowed connection timeout, and the effective wait is capped by the remaining context deadline. Metadata lives in `main` with default prefix `__xmigrator_`. The application owns and closes its DB.

Hooks receive `sqdriver.Tx`, a pinned connection executor with ExecContext, QueryContext, QueryRowContext, and PrepareContext. It is not `*sql.Tx`: the driver controls BEGIN IMMEDIATE and commit/rollback. Close rows and prepared statements opened by a hook.

File databases use a canonical-path OS lock for the entire run and a persistent adjacent `.xmigrator.lock` file. Do not remove that lock file. Locks assume local filesystems; hard links, database renames while open, and network filesystems are outside the guarantees.

For private in-memory SQLite, explicitly set `Config{Memory: true}` and `db.SetMaxOpenConns(1)`. For shared named in-memory databases, give all handles the same absolute `LockIdentity`. Do not claim a shared database is private to bypass locking.

For a parent-table rebuild, declare `-- xmigrator:sqlite-foreign-keys=off` in the appropriate script header. The driver changes FK enforcement before the transaction, runs a full foreign_key_check before commit for this explicit mode, and restores borrowed connection settings. Violations roll back SQL and history. journal_mode=OFF is rejected.

Drop clears application objects in `main`, including virtual tables, and preserves xmigrator metadata and SQLite system objects. Attached databases are outside scope. Supported SQLite baseline is 3.37+.
