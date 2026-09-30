# Core API and migration files

## Modules and source

Import `github.com/sxwebdev/xmigrator`. The core uses only the standard library and requires Go 1.27.0; this checkout selects toolchain go1.27.1.

`NewSource(files fs.FS, dir string, options ...SourceOption) (Source, error)` accepts `embed.FS`, `os.DirFS`, and other filesystem implementations. Use FS-relative paths: `NewSource(os.DirFS("./migrations"), ".")` reads a disk directory. Use `WithDialect("sqlite")` for standalone SQLite source validation; default lexical rules are pgx, and built-in runners infer their driver dialect. The constructor validates FS/path; snapshot and validation read files. Each runner operation gets its own snapshot so file edits cannot replace SQL partway through that operation.

`New[T](source, driver, options...)` constructs a typed migrator without connecting to the database. The driver determines the executor type. Use the same factory for startup and an application's CLI so hooks, logger, metadata placement, and policies agree.

## Operations

| API | Behavior |
| --- | --- |
| `Up(ctx, 0)` | Apply all pending versions in ascending version order |
| `Up(ctx, n)` | Apply at most n pending versions; n must be positive |
| `Down(ctx, n)` | Roll back at most n applied migrations in reverse actual application order; n must be positive |
| `DownAll(ctx)` | Roll back all applied migrations |
| `Drop(ctx)` | Clear the driver's configured application scope without executing down SQL or hooks |
| `Status(ctx)` | Inspect source and history without creating metadata |
| `PlanRepairDown(ctx, version)` | Preview acceptance of the current down definition |
| `RepairDown(ctx, version)` | Accept the current down checksum, kind, and hook revision under the run lock |
| `Validate(ctx, source)` | Validate source files without a DB or Go callback registry, including dialect-specific transaction safety checks |
| `Create(ctx, CreateOptions{Dir, Name, Version})` | Create an exclusive up/down pair in an existing directory |

Versions are a set, not a high-water mark. A new version 200 remains pending even after 300 was applied. Down reverses the actual application sequence. Ensure late SQL remains valid after newer changes.

Each migration commits independently. `Result.Actions` includes confirmed commits even if a later step or cleanup fails. Use `errors.Is` for sentinels and `errors.AsType[*MigrationError]` for Version, Name, Direction, and Stage; source validation exposes File/Version through SourceError. For `ErrCommitOutcomeUnknown`, inspect history under the run lock before deciding whether to retry; external hook effects cannot be inferred from history.

## File contract

```text
100_create_users.up.sql
100_create_users.down.sql
```

Use positive int64 versions and lowercase ASCII snake_case names matching `[a-z][a-z0-9]*(?:_[a-z0-9]+)*`. Numeric aliases such as `001` and `1` are the same version. Both files must exist and have the same name. The loader reads one directory without recursion, rejects directories and malformed SQL filenames, and ignores other files.

`Create` uses a UTC timestamp when Version is zero. It writes TODO templates that must be replaced before validation succeeds. Exclusive creation and a second version check guard concurrent successful calls; a crash can leave an incomplete or duplicate pair requiring explicit cleanup.

Supported header directives:

```sql
-- xmigrator:noop
```

Use noop when there is deliberately no SQL. It can still have registered hooks.

```sql
-- xmigrator:irreversible
```

Use irreversible only in a down file with no SQL, hook revision, or foreign-key directive. Down then returns `ErrIrreversible`.

```sql
-- xmigrator:hooks=seed-1
-- xmigrator:sqlite-foreign-keys=off
```

Put directives on standalone line comments before the first SQL token. Names, prefix, and values are case-sensitive. Each directive may occur once; combine distinct directives where compatible. Hook revisions match `[A-Za-z0-9][A-Za-z0-9._-]{0,127}`. The foreign-key value is `on` or `off` and is SQLite-only. Noop/irreversible cannot accompany executable SQL. Empty files and ordinary comments alone are invalid.

One initial UTF-8 BOM is removed and CRLF becomes LF. The checksum covers normalized content, including comments. SQL is executed as a script; do not split it at semicolons because functions and triggers contain internal statements.

The driver owns transaction control. PostgreSQL SET LOCAL and SET CONSTRAINTS are supported; LOCAL changes to standard_conforming_strings/session_replication_role are rejected. Do not emit top-level BEGIN/COMMIT/ROLLBACK, session SET/RESET, unsafe PRAGMAs, VACUUM, ATTACH/DETACH, nontransactional PostgreSQL concurrent index operations, or COPY streaming. Function/trigger bodies and CASE expressions have dialect-aware handling; file validation is not a complete SQL parser.

## Policies, repair, and logging

Use typed options with the driver's executor type:

- `WithChecksumPolicy[T](ChecksumStrict)`: default; reject relevant checksum differences.
- `ChecksumWarn`: allow SQL checksum differences and report issues.
- `ChecksumDisabled`: skip comparing checksums; still compute and store them.
- `WithUnknownAppliedPolicy[T](UnknownAppliedAllow)`: default; Up can proceed with unknown history versions and report issues.
- `UnknownAppliedError`: reject unknown history versions during Up.

Checksum policy does not disable name, hook revision, or down-kind checks. Up ignores down-side definition differences; Down checks both sides. A selected unknown down always fails.

Repair reads only the current source's down definition; it neither executes SQL nor changes up metadata. A name mismatch blocks repair. Up checksum/revision differences remain issues and can still block a later Down. Preview first, then apply the reviewed change. `RepairResult.Confirmed` can be true with a cleanup error; do not equate `err != nil` with rollback.

`WithLogger[T]` accepts `Debugw/Infow/Warnw/Errorw(string, ...any)`. Missing or typed-nil loggers are no-ops. Wrap `*slog.Logger` with `NewSlogLogger`. Core logs avoid SQL, DSNs, and raw backend/hook errors; the returned error remains available to the caller.

History defaults to a private metadata schema/prefix and has an ownership marker and format version. It is incompatible with golang-migrate history and older copied runners. Do not edit metadata manually to bypass validation or assume an empty new history is safe on an existing application schema.
