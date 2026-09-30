// Package app assembles the installable xmigrator command.
package app

import (
	"context"
	"database/sql"
	"fmt"
	"io"
	"log/slog"
	"os"
	"time"

	"github.com/jackc/pgx/v5"
	x "github.com/sxwebdev/xmigrator"
	"github.com/sxwebdev/xmigrator/cli/urfavecli"
	pgdriver "github.com/sxwebdev/xmigrator/driver/pgx"
	sqdriver "github.com/sxwebdev/xmigrator/driver/sqlite"
	cli "github.com/urfave/cli/v3"
	_ "modernc.org/sqlite"
)

func source(_ context.Context, c *cli.Command) (x.Source, error) {
	dialect := c.String("driver")
	return x.NewSource(os.DirFS(c.String("path")), ".", x.WithDialect(dialect))
}

func policies(c *cli.Command) (x.ChecksumPolicy, x.UnknownAppliedPolicy, error) {
	var p x.ChecksumPolicy
	switch c.String("checksum-policy") {
	case "strict":
		p = x.ChecksumStrict
	case "warn":
		p = x.ChecksumWarn
	case "disabled":
		p = x.ChecksumDisabled
	default:
		return 0, 0, x.ErrInvalidConfig
	}
	var u x.UnknownAppliedPolicy
	switch c.String("unknown-applied") {
	case "allow":
		u = x.UnknownAppliedAllow
	case "error":
		u = x.UnknownAppliedError
	default:
		return 0, 0, x.ErrInvalidConfig
	}
	return p, u, nil
}

type dependencies struct {
	source  func(context.Context, *cli.Command) (x.Source, error)
	open    func(string, string) (*sql.DB, error)
	pg      func(string, pgdriver.Config) (*pgdriver.Driver, error)
	command func(urfavecli.Config) (*cli.Command, error)
}

func defaults() dependencies {
	return dependencies{source, sql.Open, pgdriver.FromDSN, urfavecli.Command}
}

func Run(ctx context.Context, args []string, out, errout io.Writer, version string) error {
	return run(ctx, args, out, errout, version, defaults())
}

func run(ctx context.Context, args []string, out, errout io.Writer, version string, deps dependencies) error {
	logger := x.NewSlogLogger(slog.New(slog.NewTextHandler(errout, nil)))
	flags := []cli.Flag{
		&cli.StringFlag{Name: "driver", Value: "pgx"}, &cli.StringFlag{Name: "dsn", Sources: cli.EnvVars("XMIGRATOR_DSN")},
		&cli.StringFlag{Name: "metadata-prefix", Value: "__xmigrator_"}, &cli.StringFlag{Name: "metadata-schema", Value: "xmigrator"}, &cli.BoolFlag{Name: "require-existing-metadata"},
		&cli.StringSliceFlag{Name: "schema"}, &cli.StringFlag{Name: "checksum-policy", Value: "strict"}, &cli.StringFlag{Name: "unknown-applied", Value: "allow"},
		&cli.DurationFlag{Name: "lock-wait-timeout"}, &cli.DurationFlag{Name: "ddl-lock-timeout"}, &cli.DurationFlag{Name: "statement-timeout"}, &cli.DurationFlag{Name: "busy-timeout", Value: 5 * time.Second}, &cli.DurationFlag{Name: "timeout"},
	}
	resolve := func(ctx context.Context, c *cli.Command) (urfavecli.Target, error) {
		p, u, e := policies(c)
		if e != nil {
			return urfavecli.Target{}, e
		}
		for _, name := range []string{"lock-wait-timeout", "ddl-lock-timeout", "statement-timeout", "busy-timeout", "timeout"} {
			if c.Duration(name) < 0 {
				return urfavecli.Target{}, x.ErrInvalidConfig
			}
		}
		dsn := c.String("dsn")
		if dsn == "" {
			return urfavecli.Target{}, fmt.Errorf("%w: dsn required", x.ErrInvalidConfig)
		}
		ctxCancel := func() {}
		if timeout := c.Duration("timeout"); timeout > 0 {
			var cancel context.CancelFunc
			ctx, cancel = context.WithTimeout(ctx, timeout)
			ctxCancel = cancel
		}
		s, e := deps.source(ctx, c)
		if e != nil {
			ctxCancel()
			return urfavecli.Target{}, e
		}
		switch c.String("driver") {
		case "pgx":
			if c.IsSet("busy-timeout") {
				ctxCancel()
				return urfavecli.Target{}, x.ErrInvalidConfig
			}
			d, e := deps.pg(dsn, pgdriver.Config{MetadataSchema: c.String("metadata-schema"), MetadataPrefix: c.String("metadata-prefix"), RequireExistingMetadata: c.Bool("require-existing-metadata"), DropSchemas: c.StringSlice("schema"), LockWaitTimeout: c.Duration("lock-wait-timeout"), DDLLockTimeout: c.Duration("ddl-lock-timeout"), StatementTimeout: c.Duration("statement-timeout")})
			if e != nil {
				ctxCancel()
				return urfavecli.Target{}, e
			}
			m, e := x.New(s, d, x.WithChecksumPolicy[pgx.Tx](p), x.WithUnknownAppliedPolicy[pgx.Tx](u), x.WithLogger[pgx.Tx](logger))
			if e != nil {
				ctxCancel()
				return urfavecli.Target{}, e
			}
			return urfavecli.Target{Runner: boundedRunner{m, ctx}, Cleanup: func(context.Context) error { ctxCancel(); return nil }, Scope: c.StringSlice("schema")}, nil
		case "sqlite":
			for _, name := range []string{"metadata-schema", "require-existing-metadata", "schema", "ddl-lock-timeout", "statement-timeout"} {
				if c.IsSet(name) {
					ctxCancel()
					return urfavecli.Target{}, x.ErrInvalidConfig
				}
			}
			db, e := deps.open("sqlite", dsn)
			if e != nil {
				ctxCancel()
				return urfavecli.Target{}, e
			}
			closeDB := func(context.Context) error { ctxCancel(); return db.Close() }
			d, e := sqdriver.FromDB(db, sqdriver.Config{MetadataPrefix: c.String("metadata-prefix"), BusyTimeout: c.Duration("busy-timeout"), LockWaitTimeout: c.Duration("lock-wait-timeout")})
			if e != nil {
				return urfavecli.Target{Cleanup: closeDB, Scope: []string{"main"}}, e
			}
			m, e := x.New(s, d, x.WithChecksumPolicy[sqdriver.Tx](p), x.WithUnknownAppliedPolicy[sqdriver.Tx](u), x.WithLogger[sqdriver.Tx](logger))
			return urfavecli.Target{Runner: boundedRunner{m, ctx}, Cleanup: closeDB, Scope: []string{"main"}}, e
		default:
			ctxCancel()
			return urfavecli.Target{}, fmt.Errorf("%w: unknown driver", x.ErrInvalidConfig)
		}
	}
	command, e := deps.command(urfavecli.Config{Name: "xmigrator", Resolve: resolve, ResolveSource: deps.source, Flags: flags, CreateDir: "./sql/migrations", EnableDrop: true})
	if e != nil {
		return e
	}
	// Path belongs to filesystem commands as well as backend resolvers, avoiding a duplicate adapter flag.
	for _, child := range command.Commands {
		if child.Name != "create" {
			child.Flags = append(child.Flags, &cli.StringFlag{Name: "path", Value: "./sql/migrations"})
		}
	}
	command.Writer = out
	command.ErrWriter = errout
	command.Version = version
	command.ExitErrHandler = func(context.Context, *cli.Command, error) {}
	return command.Run(ctx, args)
}

// boundedRunner applies the resolver's whole-operation deadline without a background goroutine.
type boundedRunner struct {
	urfavecli.Runner
	ctx context.Context
}

func (r boundedRunner) Up(_ context.Context, n int) (x.Result, error) { return r.Runner.Up(r.ctx, n) }

func (r boundedRunner) Down(_ context.Context, n int) (x.Result, error) {
	return r.Runner.Down(r.ctx, n)
}
func (r boundedRunner) DownAll(context.Context) (x.Result, error) { return r.Runner.DownAll(r.ctx) }
func (r boundedRunner) Drop(context.Context) error                { return r.Runner.Drop(r.ctx) }
func (r boundedRunner) Status(context.Context) (x.Status, error)  { return r.Runner.Status(r.ctx) }
func (r boundedRunner) PlanRepairDown(_ context.Context, v x.Version) (x.RepairPlan, error) {
	return r.Runner.PlanRepairDown(r.ctx, v)
}

func (r boundedRunner) RepairDown(_ context.Context, v x.Version) (x.RepairResult, error) {
	return r.Runner.RepairDown(r.ctx, v)
}
