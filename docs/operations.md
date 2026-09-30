# History and operations

## Metadata schema

By default, PostgreSQL stores metadata in the xmigrator schema and SQLite in main. The `__xmigrator_` prefix separates metadata tables from application tables. Configure MetadataSchema (PostgreSQL) and MetadataPrefix in the driver, and keep them consistent across processes operating on the same history.

`__xmigrator_meta` contains one row: id=1, owner_id=`github.com/sxwebdev/xmigrator`, format_version=1. The driver checks the marker, table structure, and constraints. Incompatible or unrelated objects return ErrMetadataConflict/ErrMetadataVersion and are not adopted automatically.

`__xmigrator_history` contains one row per applied migration:

| Field | Meaning |
| --- | --- |
| version | Unique positive version |
| name | Name from the source |
| up_checksum / down_checksum | SHA-256, 32 bytes |
| down_kind | sql / noop / irreversible |
| up_hook_revision / down_hook_revision | Callback revisions; empty without hooks |
| applied_at | Application time |
| apply_order | Actual application order |

PostgreSQL uses version BIGINT PRIMARY KEY, apply_order BIGINT identity UNIQUE, BYTEA hashes, and TIMESTAMPTZ timestamps. SQLite uses apply_order INTEGER PRIMARY KEY AUTOINCREMENT, version INTEGER UNIQUE, BLOB hashes, and TEXT timestamps. Down removes the history row; a subsequent Up gets a new apply_order.

There is no dirty flag: SQL, hooks, and history commit in one transaction. Up/Down create metadata when needed. Status, PlanRepairDown, and RepairDown do not create tables on a clean database; repair returns ErrNotApplied. Drop preserves metadata objects and clears history together with application data in its transaction.

## Error diagnostics

Handle the error together with the operation result. Result.Actions contains confirmed commits, and cleanup may fail after a successful commit. Do not repeat the entire run based solely on a non-nil error.

MigrationError contains Version, Name, Direction, Stage, and the underlying Err with Unwrap. SourceError reports File, Version, and the source failure cause. Use errors.Is for sentinels and errors.As/errors.AsType for context.

ErrCommitOutcomeUnknown means the backend did not confirm the outcome. Read Status after restoring the connection and determine whether the history record exists. Do not assume the migration rolled back. An explicit PostgreSQL COMMIT rejection with severity ERROR, and a confirmed SQLite ROLLBACK after a COMMIT error, are treated as known rollbacks.

Status also reports missing_hooks/hook_registration_mismatch without invoking callbacks. The CLI displays identifiers and safe backend error categories without SQL or user data. Applications can access the underlying error; choose log contents according to your application's requirements.

## Repairing down

Normally, changes after application should be expressed as new migrations. If you need to fix a rollback, first update the down file and its hooks/revision, then:

1. Call PlanRepairDown or CLI repair-down --dry-run.
2. Review Before, After, and Issues.
3. Call RepairDown or CLI repair-down --yes.
4. Run Down with the current registry.

Repair runs under the run lock and accepts only the current down checksum, kind, and hook revision from the validated source. It does not execute SQL or change up metadata. An up mismatch does not block repair; it remains in Issues and is checked by a normal Down.

RepairResult.Confirmed indicates a confirmed commit; Changed indicates a metadata change. Confirmed=true with an error is possible after cleanup fails, and After is already applied. In a read-only plan, After is a candidate. No separate audit log is maintained: retain the result or acceptance log according to your application's requirements.

Warn/Disabled only change SQL checksum verification. Changing down kind/revision requires repair regardless of checksum policy.

## Drop

Drop does not execute down SQL or hooks and does not use the source as an object list. PostgreSQL requires explicit application DropSchemas; SQLite clears main. Scope also includes objects created outside the migrator. See [drivers](drivers.md) for backend restrictions.

## Switching from another library

History from previous migrators or golang-migrate is not imported automatically. Changing a prefix or schema selects a different history location; it does not transfer state. On a populated database with empty xmigrator history, every source migration is pending.

golang-migrate stores a current version and dirty state. xmigrator stores the applied set, actual apply_order, both SQL checksums, and hook revisions. The maximum old version does not prove which skipped versions actually ran or in what order.

There is no built-in adopt/baseline operation. Existing applications need a separately verified state transfer, schema/data checks, and a backup. Do not use ordinary Up as an automatic import or alternate between libraries: after new operations, the previous history becomes stale.
