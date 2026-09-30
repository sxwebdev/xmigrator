// Package urfavecli integrates xmigrator with urfave/cli v3 without importing SQL drivers.
package urfavecli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"time"

	x "github.com/sxwebdev/xmigrator"
	cli "github.com/urfave/cli/v3"
)

type Runner interface {
	Up(context.Context, int) (x.Result, error)
	Down(context.Context, int) (x.Result, error)
	DownAll(context.Context) (x.Result, error)
	Drop(context.Context) error
	Status(context.Context) (x.Status, error)
	PlanRepairDown(context.Context, x.Version) (x.RepairPlan, error)
	RepairDown(context.Context, x.Version) (x.RepairResult, error)
}
type Target struct {
	Runner  Runner
	Cleanup func(context.Context) error
	Scope   []string
}
type Config struct {
	Name          string
	Dialect       string
	Flags         []cli.Flag
	Resolve       func(context.Context, *cli.Command) (Target, error)
	ResolveSource func(context.Context, *cli.Command) (x.Source, error)
	CreateDir     string
	EnableDrop    bool
}

// Command returns a fresh command tree. Results use the caller's urfave writer.
func Command(cfg Config) (*cli.Command, error) {
	if cfg.Resolve == nil || cfg.ResolveSource == nil || cfg.Dialect != "" && cfg.Dialect != "pgx" && cfg.Dialect != "sqlite" {
		return nil, x.ErrInvalidConfig
	}
	if cfg.Name == "" {
		cfg.Name = "migrations"
	}
	reserved := map[string]bool{}
	for _, name := range []string{"help", "h", "version", "v", "steps", "all", "yes", "dry-run", "name", "path"} {
		reserved[name] = true
	}
	for _, f := range cfg.Flags {
		if f == nil {
			return nil, x.ErrInvalidConfig
		}
		for _, name := range f.Names() {
			if reserved[name] {
				return nil, fmt.Errorf("%w: flag %s", x.ErrInvalidConfig, name)
			}
			reserved[name] = true
		}
	}
	write := func(cmd *cli.Command, v any) error { return json.NewEncoder(cmd.Root().Writer).Encode(v) }
	run := func(operation string) cli.ActionFunc {
		return func(ctx context.Context, cmd *cli.Command) (err error) {
			if cmd.Args().Len() != 0 {
				return x.ErrInvalidConfig
			}
			steps := cmd.Int("steps")
			switch operation {
			case "up":
				if cmd.IsSet("steps") && steps <= 0 {
					return x.ErrInvalidSteps
				}
			case "down":
				if cmd.Bool("all") && cmd.IsSet("steps") {
					return x.ErrInvalidSteps
				}
				if !cmd.Bool("all") && steps <= 0 {
					return x.ErrInvalidSteps
				}
			case "drop":
				if !cmd.Bool("yes") {
					return x.ErrInvalidConfig
				}
			case "repair-down":
				if cmd.Int64("version") <= 0 || cmd.Bool("yes") == cmd.Bool("dry-run") {
					return x.ErrInvalidConfig
				}
			}
			target, e := cfg.Resolve(ctx, cmd)
			if target.Cleanup != nil {
				defer func() {
					cc, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
					defer cancel()
					err = errors.Join(err, target.Cleanup(cc))
				}()
			}
			if e != nil {
				return e
			}
			if target.Runner == nil || reflect.ValueOf(target.Runner).Kind() == reflect.Pointer && reflect.ValueOf(target.Runner).IsNil() {
				return x.ErrInvalidConfig
			}
			switch operation {
			case "up":
				r, e := target.Runner.Up(ctx, steps)
				return errors.Join(e, write(cmd, r))
			case "down":
				var r x.Result
				var e error
				if cmd.Bool("all") {
					r, e = target.Runner.DownAll(ctx)
				} else {
					r, e = target.Runner.Down(ctx, steps)
				}
				return errors.Join(e, write(cmd, r))
			case "status":
				r, e := target.Runner.Status(ctx)
				if e != nil {
					return e
				}
				return write(cmd, r)
			case "drop":
				if e = target.Runner.Drop(ctx); e != nil {
					return e
				}
				return write(cmd, struct {
					Dropped bool
					Scope   []string
				}{true, target.Scope})
			default: // The private dispatcher has exactly five non-repair cases.
				v := x.Version(cmd.Int64("version"))
				if cmd.Bool("dry-run") {
					r, e := target.Runner.PlanRepairDown(ctx, v)
					if e != nil {
						return e
					}
					return write(cmd, r)
				}
				r, e := target.Runner.RepairDown(ctx, v)
				if r.Confirmed {
					return errors.Join(e, write(cmd, r))
				}
				return e
			}
		}
	}
	extra := func(flags ...cli.Flag) []cli.Flag { return append(append([]cli.Flag{}, cfg.Flags...), flags...) }
	children := []*cli.Command{
		{Name: "up", Usage: "Apply pending migrations", Flags: extra(&cli.IntFlag{Name: "steps", Config: cli.IntegerConfig{Base: 10}}), Action: run("up")},
		{Name: "down", Usage: "Revert in reverse application order", Flags: extra(&cli.IntFlag{Name: "steps", Value: 1, Config: cli.IntegerConfig{Base: 10}}, &cli.BoolFlag{Name: "all"}), Action: run("down")},
		{Name: "status", Usage: "Inspect source and current history", Flags: extra(), Action: run("status")},
		{Name: "repair-down", Usage: "Preview or accept the current down definition", Flags: extra(&cli.Int64Flag{Name: "version", Config: cli.IntegerConfig{Base: 10}}, &cli.BoolFlag{Name: "dry-run"}, &cli.BoolFlag{Name: "yes"}), Action: run("repair-down")},
		{Name: "create", Usage: "Create an unimplemented migration pair", Flags: []cli.Flag{&cli.StringFlag{Name: "path", Value: cfg.CreateDir}, &cli.StringFlag{Name: "name", Required: true}, &cli.Int64Flag{Name: "version", Config: cli.IntegerConfig{Base: 10}}}, Action: func(ctx context.Context, cmd *cli.Command) error {
			if cmd.Args().Len() != 0 {
				return x.ErrInvalidConfig
			}
			files, e := x.Create(ctx, x.CreateOptions{Dir: cmd.String("path"), Name: cmd.String("name"), Version: x.Version(cmd.Int64("version"))})
			if e != nil {
				return e
			}
			return write(cmd, files)
		}},
		{Name: "validate", Usage: "Validate SQL files without connecting", Flags: extra(), Action: func(ctx context.Context, cmd *cli.Command) error {
			if cmd.Args().Len() != 0 {
				return x.ErrInvalidConfig
			}
			source, e := cfg.ResolveSource(ctx, cmd)
			if e != nil {
				return e
			}
			if cfg.Dialect != "" {
				source, _ = source.WithDialect(cfg.Dialect)
			} else if source.Dialect() == "" {
				return fmt.Errorf("%w: validate requires Config.Dialect or an explicitly configured source dialect", x.ErrInvalidConfig)
			}
			report, e := x.Validate(ctx, source)
			if e != nil {
				return e
			}
			return write(cmd, report)
		}},
	}
	if cfg.EnableDrop {
		children = append(children, &cli.Command{Name: "drop", Usage: "Clear the explicitly selected database scope", Flags: extra(&cli.BoolFlag{Name: "yes"}), Action: run("drop")})
	}
	return &cli.Command{Name: cfg.Name, Usage: "Transactional SQL migrations", Commands: children}, nil
}
