# Migration files and hooks

## Filenames and SQL

Place migration pairs in a single directory:

```text
100_create_users.up.sql
100_create_users.down.sql
```

A version is a positive int64, unique across the source. Names must match `[a-z][a-z0-9]*(?:_[a-z0-9]+)*`. Both files are required and must share the same version and name. Files without the `.sql` suffix are ignored; an invalid SQL filename or incomplete pair returns an error.

`100_create_users.up.sql`:

```sql
CREATE TABLE users (
    id BIGINT PRIMARY KEY,
    name TEXT NOT NULL
);
```

`100_create_users.down.sql`:

```sql
DROP TABLE users;
```

The complete SQL script is executed without splitting it at semicolons. Empty SQL is only valid with an explicit directive. One leading UTF-8 BOM is removed; a repeated BOM is rejected. CRLF is normalized to LF. SHA-256 is computed over the complete normalized text, including comments and directives.

## Directives

Directives are standalone line comments before the first SQL token. Spaces or tabs may precede `--`. The prefix, keys, and values are case-sensitive; do not insert spaces within `key=value`. Each key may appear once; place different directives on separate lines. Ordinary comments may precede SQL. Unknown directives are rejected.

| Directive | Purpose |
| --- | --- |
| `-- xmigrator:noop` | Skip SQL for this phase; hooks may still run |
| `-- xmigrator:irreversible` | Down only: reject rollback |
| `-- xmigrator:hooks=seed-1` | Go hook revision for this phase |
| `-- xmigrator:sqlite-foreign-keys=off` | SQLite: disable FK enforcement before the transaction and check the entire database before commit |
| `-- xmigrator:sqlite-foreign-keys=on` | SQLite: explicitly enable FK enforcement for this phase |

`noop` and `irreversible` cannot be combined with SQL or with each other. An irreversible down cannot declare hooks or an FK directive. Hook revisions must match `[A-Za-z0-9][A-Za-z0-9._-]{0,127}`.

For example, the down file of a migration with no reversible operation contains only:

```sql
-- xmigrator:irreversible
```

## Transaction ownership

Do not add BEGIN, COMMIT, ROLLBACK, SAVEPOINT, or their equivalents: the driver owns the transaction. Commands that violate this contract are rejected, including VACUUM, session SET/RESET, COPY STDIN/STDOUT, SQLite ATTACH/DETACH, and changes to critical PRAGMA settings. PostgreSQL CREATE/DROP INDEX CONCURRENTLY and REINDEX CONCURRENTLY are unsupported.

PostgreSQL SET LOCAL and SET CONSTRAINTS are allowed, except for changes to standard_conforming_strings and session_replication_role. REFRESH MATERIALIZED VIEW CONCURRENTLY is allowed. PostgreSQL function bodies using BEGIN ATOMIC, SQLite trigger bodies using BEGIN/END, and CASE END are distinguished from outer transaction control.

Source validation is not a complete SQL parser or sandbox. Do not bypass transaction ownership through dynamic SQL, server functions, or hooks.

## Typed hooks

Use hooks for transformations that benefit from Go code, such as serialization, computing values, or validating data. Keep simple inserts in SQL.

Add `-- xmigrator:hooks=seed-1` before CREATE TABLE in `100_create_users.up.sql`, then register the callback:

```go
runner, err := xmigrator.New(source, driver,
    xmigrator.WithHooks[pgx.Tx](map[xmigrator.Version]xmigrator.Hooks[pgx.Tx]{
        100: {
            UpRevision: "seed-1",
            AfterUp: func(ctx context.Context, tx pgx.Tx) error {
                _, err := tx.Exec(ctx,
                    "INSERT INTO users (id, name) VALUES ($1, $2)",
                    1, "example",
                )
                return err
            },
        },
    }),
)
```

Here, `pgx.Tx` is imported from `github.com/jackc/pgx/v5`; SQLite uses `sqlite.Tx` from its driver. The down file with DROP TABLE needs no callback or hooks directive.

Execution order is BEGIN → BeforeUp/BeforeDown → SQL → AfterUp/AfterDown → history insert/delete → COMMIT. A callback error rolls back that step's SQL and history. Use the supplied executor, close Rows/Stmt, and do not call Commit/Rollback. External HTTP requests, messages, and filesystem writes do not roll back with the database.

UpRevision covers both up callbacks; DownRevision covers both down callbacks. A revision is a manually assigned identifier, not a function name or automatic hash. A callback requires a matching revision in the registry and its file; without callbacks, the revision is empty. Registering the same version more than once through WithHooks returns an error.

Up/Down check the registry for the entire source, including unselected steps. Revisions are stored in history. Changing code without changing its revision cannot be detected automatically. Accept a changed down definition through explicit [repair](operations.md#repairing-down).

The standalone CLI cannot execute application Go callbacks. Use application startup or an embedded CLI with the same registry for these migrations. Status, Validate, and repair work without callbacks; successful file validation does not prove that the standalone binary can execute a source with hooks.

## Creating files

```go
files, err := xmigrator.Create(ctx, xmigrator.CreateOptions{
    Dir: "./migrations",
    Name: "create_users",
})
```

The directory must already exist. Without Version, Create uses a UTC timestamp in YYYYMMDDhhmmss format. Both generated files contain TODO comments: fill them with SQL or directives before validate/up. A version collision returns ErrVersionExists, including equal versions with different names.

Create uses exclusive files and a directory recheck. A process crash may leave an incomplete pair; restore or remove it before validation. Use Validate in CI to detect collisions after merging branches.
