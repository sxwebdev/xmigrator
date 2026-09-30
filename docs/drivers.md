# Drivers

## PostgreSQL

The `github.com/sxwebdev/xmigrator/driver/pgx` package uses native pgx v5 and supports PostgreSQL 14+.

Constructors are `FromPool(*pgxpool.Pool, Config)`, `FromConfig(*pgx.ConnConfig, Config)`, and `FromDSN(string, Config)`. They do not connect to the server. FromConfig copies the pgx configuration. FromPool preserves a custom dialer, acquires and hijacks one connection per run, and physically closes it afterward. The application retains ownership of the pool. Hooks receive `pgx.Tx`.

| Config | Purpose |
| --- | --- |
| MetadataSchema | History schema; default `xmigrator` |
| MetadataPrefix | Table prefix; default `__xmigrator_` |
| RequireExistingMetadata | Disable automatic metadata creation |
| DropSchemas | Explicit application schema list for Drop |
| LockWaitTimeout | Run lock acquisition timeout |
| DDLLockTimeout | PostgreSQL lock_timeout during execution |
| StatementTimeout | PostgreSQL statement_timeout during execution |

Timeouts are time.Duration values. DDL/statement timeouts must be whole milliseconds. Zero preserves inherited backend settings; negative values are rejected. A zero LockWaitTimeout sets no separate bound: use a context deadline.

A session advisory lock covers the entire run, and waiting can be cancelled through context. Use direct connections or session pooling; transaction/statement pooling cannot preserve the required session.

Drop requires a nonempty DropSchemas list. It removes supported application objects while preserving the schemas and their grants. Metadata and system schemas cannot be included. External dependencies, extension-owned objects, and unsupported object classes reject the operation atomically. Scope is not inferred from migration files.

For a service role without bootstrap privileges, provision metadata with an administrative driver:

```go
err := adminDriver.WithSession(ctx, func(session xmigrator.Session[pgx.Tx]) error {
    return session.EnsureMetadata(ctx)
})
```

Grant the service role USAGE on the metadata schema, SELECT/INSERT/UPDATE/DELETE on both tables, and USAGE/SELECT on the history identity sequence. Set RequireExistingMetadata on the service driver. Application object privileges depend on the migration SQL.

## SQLite

The `github.com/sxwebdev/xmigrator/driver/sqlite` package uses `database/sql` and supports SQLite 3.37+. The application registers its own engine; the package does not import modernc or mattn.

```go
source, err := xmigrator.NewSource(files, "migrations", xmigrator.WithDialect("sqlite"))
if err != nil {
    return err
}
driver, err := sqlite.FromDB(db, sqlite.Config{})
if err != nil {
    return err
}
runner, err := xmigrator.New(source, driver)
if err != nil {
    return err
}
_, err = runner.Up(ctx, 0)
```

Here, files is an fs.FS and db is an open `*sql.DB`; sqlite is imported from the driver package. The application retains ownership of the DB.

| Config | Purpose |
| --- | --- |
| MetadataPrefix | Table prefix in main; default `__xmigrator_` |
| BusyTimeout | SQLITE_BUSY timeout in whole milliseconds |
| LockWaitTimeout | Run lock acquisition timeout |
| Memory | Private in-memory mode with MaxOpenConns=1 |
| LockIdentity | Shared absolute lock path for shared-memory handles |

BusyTimeout=0 preserves the borrowed connection's settings; the effective positive timeout is capped by the remaining context deadline. Connection settings are restored after execution. Negative timeouts are rejected.

Transactions use BEGIN IMMEDIATE. Hooks receive `sqlite.Tx` with ExecContext, QueryContext, QueryRowContext, and PrepareContext. This is a pinned connection executor, not `*sql.Tx`. Close Rows and Stmt opened by a hook.

File databases use an OS lock at their canonical path and a persistent adjacent `.xmigrator.lock` file. Do not remove this file. Lock guarantees assume a local filesystem; hard links, renaming an open database, and network filesystems are outside the locking contract.

Private in-memory databases require `Memory: true` and `db.SetMaxOpenConns(1)`. All handles for a named shared-memory database must use the same absolute LockIdentity; Memory=true does not replace shared locking.

For a parent table rebuild, use the `sqlite-foreign-keys=off` header directive. FK enforcement is switched before the transaction, and a full foreign_key_check runs before commit. Violations roll back SQL and history. Ordinary migrations use normal constraints without scanning all existing data. journal_mode=OFF is rejected.

Drop clears application objects in main, including virtual tables, while preserving metadata and SQLite system objects. Attached databases are outside scope.

## Custom drivers

Driver[T] provides WithSession, ReadHistorySnapshot, and ValidateScript. Session[T] owns the run lock and pinned connection; Transaction[T] executes SQL and modifies history in the same transaction. InTx receives Script before BEGIN so it can apply connection-scoped directives.

TxOutcome is separate from error: a confirmed commit remains TxCommitted even if cleanup fails; an uncertain outcome is TxUnknown. ReadHistorySnapshot and ReadExistingHistory must not create metadata. ValidateScript must enforce transaction ownership before execution. See the interfaces in [types.go](../types.go).
