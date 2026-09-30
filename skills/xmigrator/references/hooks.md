# Typed transaction hooks

Bind hooks to the numeric migration version with `WithHooks[T](map[Version]Hooks[T])`. Use `pgx.Tx` for PostgreSQL or the SQLite driver's `Tx` interface for SQLite.

Execution order is:

```text
BEGIN
  BeforeUp / BeforeDown
  up SQL / down SQL
  AfterUp / AfterDown
  insert / delete history record
COMMIT
```

Any callback error rolls back that migration's SQL and history. Do not call Commit/Rollback or open a separate connection for migration writes. HTTP requests, messages, and filesystem writes cannot be rolled back with the DB; avoid making them part of an atomicity guarantee.

## Example: seed data with computed settings

`100_seed_users.up.sql`:

```sql
-- xmigrator:hooks=seed-1
INSERT INTO users (id, name) VALUES (1, 'example');
```

`100_seed_users.down.sql`:

```sql
DELETE FROM users WHERE id = 1;
```

Register only an up callback, so only the up file declares a hook revision:

```go
package database

import (
    "context"
    "encoding/json"

    "github.com/jackc/pgx/v5"
    "github.com/sxwebdev/xmigrator"
    pgdriver "github.com/sxwebdev/xmigrator/driver/pgx"
)

func NewRunner(source xmigrator.Source, driver *pgdriver.Driver) (*xmigrator.Migrator[pgx.Tx], error) {
    return xmigrator.New(source, driver,
        xmigrator.WithHooks[pgx.Tx](map[xmigrator.Version]xmigrator.Hooks[pgx.Tx]{
            100: {
                UpRevision: "seed-1",
                AfterUp: func(ctx context.Context, tx pgx.Tx) error {
                    settings, err := json.Marshal(map[string]bool{"notifications": true})
                    if err != nil {
                        return err
                    }
                    _, err = tx.Exec(ctx,
                        "UPDATE users SET settings = $1 WHERE id = $2",
                        settings, 1,
                    )
                    return err
                },
            },
        }),
    )
}
```

The table and settings column must exist before this migration. For simple literal seed values, prefer SQL; use Go when an algorithm, serializer, or validation materially simplifies the migration.

## Revision contract

`hooks=seed-1` is a developer-maintained revision, not a function name or automatic hash. The registry entry chooses the callback; the header verifies it matches the intended Go implementation.

UpRevision covers both BeforeUp and AfterUp. DownRevision separately covers BeforeDown and AfterDown. The directions may use different revisions. At least one callback must exist when its revision is nonempty, and both the source and registry must agree. With no callbacks, omit the header and leave that direction's revision empty.

Before Up or Down, the current implementation checks both directions for every source migration, including versions not selected for this run. Missing callbacks, orphan registrations, or revision differences return `ErrHookMismatch`. Repeating a version across WithHooks calls is an error, not an override.

Revisions are saved in history. Changing callback code without changing revision cannot be detected. Prefer a new migration for changes after application; to accept a changed down definition, use explicit PlanRepairDown/RepairDown. Repair does not update the saved up revision.

Noop suppresses only SQL; callbacks still execute. An irreversible down cannot declare hooks.

The standalone binary cannot execute application Go callbacks. Embed the runner in the application's startup or CLI with the same hook registry. Status, file validation, and down repair do not require that runtime registry; a successful validate does not prove a hooked migration can run in the standalone binary.
