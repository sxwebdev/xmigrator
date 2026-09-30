# CLI integration

## Standalone command

Module: `github.com/sxwebdev/xmigrator/cmd/xmigrator`. It includes pgx and modernc SQLite; this dependency choice does not affect applications importing only the SQLite library driver.

After release tags are published:

```sh
go install github.com/sxwebdev/xmigrator/cmd/xmigrator@latest
```

Before the first release, build the checkout with `go run tools/dev.go workspace` and `make build`; the executable is `./build/xmigrator`.

```sh
mkdir -p migrations
xmigrator create --path ./migrations --name create_users
# Replace the generated TODO SQL before validating.
xmigrator validate --path ./migrations
xmigrator up --driver sqlite --dsn ./app.db --path ./migrations --steps 2
xmigrator status --driver sqlite --dsn ./app.db --path ./migrations
xmigrator down --driver sqlite --dsn ./app.db --path ./migrations --steps 1
```

Supply backend flags after the subcommand. For PostgreSQL, use `--driver pgx` and `--dsn` or environment variable `XMIGRATOR_DSN`. Omit --steps on up for all pending migrations; explicit --steps must be positive. Down defaults to one step; --all cannot be combined with --steps.

Use `repair-down --version 100 --dry-run` with the same backend/path flags to preview, then `--yes` instead of --dry-run to accept. Exactly one of these flags is required; neither means a usage error. Repair does not execute the down migration.

Drop requires --yes; PostgreSQL additionally requires explicit --schema scope. Enable and run drop only as part of the requested workflow, because it clears application objects rather than following down scripts.

Common flags include --metadata-prefix, --checksum-policy, --unknown-applied, --timeout, and --lock-wait-timeout. PostgreSQL also supports --metadata-schema, --require-existing-metadata, --ddl-lock-timeout, and --statement-timeout; SQLite supports --busy-timeout. Backend-specific flags for the other database are rejected. Use command help for exact values/defaults. Results are JSON; standalone logs go to stderr.

## urfave/cli v3 adapter

Import `urfavecli "github.com/sxwebdev/xmigrator/cli/urfavecli"` and `cli "github.com/urfave/cli/v3"`. The adapter imports no SQL driver. Its Runner interface accepts either typed migrator, so keep driver creation in the application's factory.

```go
package commands

import (
    "context"

    "github.com/sxwebdev/xmigrator"
    "github.com/sxwebdev/xmigrator/cli/urfavecli"
    cli "github.com/urfave/cli/v3"
)

func MigrationCommand(runner urfavecli.Runner, source xmigrator.Source) (*cli.Command, error) {
    return urfavecli.Command(urfavecli.Config{
        Name:      "migrations",
        Dialect:   "pgx",
        CreateDir: "./migrations",
        Resolve: func(context.Context, *cli.Command) (urfavecli.Target, error) {
            return urfavecli.Target{Runner: runner}, nil
        },
        ResolveSource: func(context.Context, *cli.Command) (xmigrator.Source, error) {
            return source, nil
        },
    })
}
```

Append the returned command to the application's root `Commands`. Both Resolve and ResolveSource are required. For validate, explicitly select Config.Dialect (pgx or sqlite), or return a source configured with WithDialect. An unspecified dialect is rejected instead of assuming PostgreSQL for a SQLite application. ResolveSource validates files without opening the database; create uses CreateDir on disk even if source is embedded. Default command name is migrations.

If Resolve opens its own connection/pool, return Target.Cleanup to close it. Cleanup runs on resolver/operation errors with a bounded uncancelled context. Do not return cleanup for a pool owned by the long-lived application. Keep its source/hooks/policies identical to startup.

Config.Flags adds application flags to database commands and validate; builtin-name and alias collisions fail construction. Results use the host root command's Writer. Drop is absent by default; enable with EnableDrop and report configured scope through Target.Scope. Scope is display information, not a replacement for the driver's actual DropSchemas.

Add future framework adapters as independent modules under distinct paths, such as cli/cobracli. Do not add framework-specific types to the core Runner/Driver contracts.
