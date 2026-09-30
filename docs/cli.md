# CLI

## Installation and usage

On macOS:

```sh
brew trust --tap https://github.com/sxwebdev/xmigrator
brew tap sxwebdev/xmigrator https://github.com/sxwebdev/xmigrator
brew install --cask xmigrator
```

Or install with Go on any supported platform:

```sh
go install github.com/sxwebdev/xmigrator/cmd/xmigrator@latest
xmigrator --version
```

The binary includes PostgreSQL and modernc SQLite. Pass backend flags after the subcommand. The default driver is pgx; supply a DSN through --dsn or XMIGRATOR_DSN.

```sh
mkdir -p migrations
xmigrator create --path ./migrations --name create_users
# Edit both generated SQL files before validation.
xmigrator validate --driver sqlite --path ./migrations
xmigrator up --driver sqlite --dsn ./app.db --path ./migrations
xmigrator status --driver sqlite --dsn ./app.db --path ./migrations
xmigrator down --driver sqlite --dsn ./app.db --path ./migrations --steps 1
```

For PostgreSQL:

```sh
export XMIGRATOR_DSN='postgres://localhost/app?sslmode=disable'
xmigrator validate --driver pgx --path ./migrations
xmigrator up --driver pgx --path ./migrations --timeout 2m
```

| Command | Options |
| --- | --- |
| up | Without steps, apply all pending migrations; --steps N requires N > 0 |
| down | Default one step; --steps N or --all, never both |
| status | Inspect the source and current history without changing the database |
| validate | Check files using --driver pgx/sqlite without a DSN |
| create | --path, --name, and optional --version |
| repair-down | --version N and exactly one of --dry-run / --yes |
| drop | --yes; PostgreSQL also requires an explicit --schema |

Database commands (status/up/down/repair-down/drop) support --metadata-prefix, --checksum-policy strict/warn/disabled, --unknown-applied allow/error, --timeout, and --lock-wait-timeout. PostgreSQL also supports --metadata-schema, --require-existing-metadata, --ddl-lock-timeout, --statement-timeout, and --schema. SQLite supports --busy-timeout, with a default of 5s. Explicit options for the other backend are rejected. Run `xmigrator <command> --help` for exact flags and defaults.

Results are JSON on stdout; logs and safe diagnostics go to stderr. Failed up/down operations may return JSON containing already confirmed steps. Check the exit code regardless of whether JSON was emitted.

Preview and accept a down definition:

```sh
xmigrator repair-down --driver sqlite --dsn ./app.db --path ./migrations --version 100 --dry-run
xmigrator repair-down --driver sqlite --dsn ./app.db --path ./migrations --version 100 --yes
```

Drop removes objects within its configured scope instead of executing down files:

```sh
xmigrator drop --driver pgx --path ./migrations --schema public --yes
```

The standalone command does not execute application Go hooks. Use an embedded runner/CLI with the hook registry for sources that declare hooks.

## urfave/cli v3

The separate `github.com/sxwebdev/xmigrator/cli/urfavecli` module imports no SQL drivers:

```sh
go get github.com/sxwebdev/xmigrator/cli/urfavecli
```

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
        Name: "migrations",
        Dialect: "pgx",
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

Append the returned command to root.Commands. Both factories are required. ResolveSource does not open the database and is used for Validate; select Config.Dialect or return a Source configured with WithDialect. An unspecified dialect is an error for this command. Create always writes to a directory on disk, even if the source is embedded.

Use the same source/driver/hooks/policies factory for startup and the CLI. If Resolve creates its own pool/DB, return Target.Cleanup; it runs on errors too, with a bounded uncancelled context. An application-owned pool needs no Cleanup.

Config.Flags adds application flags to database commands and validate; builtin name/alias collisions are rejected. Results use the root command's Writer. Drop is disabled by default and enabled through EnableDrop. Target.Scope reports the scope in output; the driver enforces the actual restrictions.
