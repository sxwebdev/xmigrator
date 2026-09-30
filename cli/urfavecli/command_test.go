package urfavecli_test

import (
	"bytes"
	"context"
	"errors"
	"os"
	"testing"

	x "github.com/sxwebdev/xmigrator"
	"github.com/sxwebdev/xmigrator/cli/urfavecli"
	cli "github.com/urfave/cli/v3"
)

var failed = errors.New("failed")

type runner struct {
	actions   []string
	steps     int
	err       error
	confirmed bool
}

func (r *runner) Up(_ context.Context, n int) (x.Result, error) {
	r.actions = append(r.actions, "up")
	r.steps = n
	return x.Result{}, r.err
}

func (r *runner) Down(_ context.Context, n int) (x.Result, error) {
	r.actions = append(r.actions, "down")
	r.steps = n
	return x.Result{}, r.err
}

func (r *runner) DownAll(context.Context) (x.Result, error) {
	r.actions = append(r.actions, "down-all")
	return x.Result{}, r.err
}
func (r *runner) Drop(context.Context) error { r.actions = append(r.actions, "drop"); return r.err }
func (r *runner) Status(context.Context) (x.Status, error) {
	r.actions = append(r.actions, "status")
	return x.Status{}, r.err
}

func (r *runner) PlanRepairDown(context.Context, x.Version) (x.RepairPlan, error) {
	r.actions = append(r.actions, "preview")
	return x.RepairPlan{WouldChange: true}, r.err
}

func (r *runner) RepairDown(context.Context, x.Version) (x.RepairResult, error) {
	r.actions = append(r.actions, "repair")
	return x.RepairResult{Confirmed: r.confirmed, Changed: r.confirmed}, r.err
}

func config(r *runner, dir string) urfavecli.Config {
	return urfavecli.Config{Dialect: "pgx", CreateDir: dir, EnableDrop: true, Resolve: func(context.Context, *cli.Command) (urfavecli.Target, error) { return urfavecli.Target{Runner: r}, nil }, ResolveSource: func(context.Context, *cli.Command) (x.Source, error) { return x.NewSource(os.DirFS(dir), ".") }}
}

func invoke(t *testing.T, cfg urfavecli.Config, args ...string) (string, error) {
	t.Helper()
	c, e := urfavecli.Command(cfg)
	if e != nil {
		t.Fatal(e)
	}
	var out bytes.Buffer
	c.Writer = &out
	c.ErrWriter = &out
	c.ExitErrHandler = func(context.Context, *cli.Command, error) {}
	e = c.Run(t.Context(), append([]string{"migrations"}, args...))
	return out.String(), e
}

func TestCommands(t *testing.T) {
	for _, tt := range []struct {
		name   string
		args   []string
		action string
		steps  int
	}{
		{"up", []string{"up"}, "up", 0}, {"up_steps", []string{"up", "--steps", "2"}, "up", 2}, {"down", []string{"down"}, "down", 1}, {"down_steps", []string{"down", "--steps", "3"}, "down", 3}, {"down_all", []string{"down", "--all"}, "down-all", 0}, {"status", []string{"status"}, "status", 0}, {"drop", []string{"drop", "--yes"}, "drop", 0}, {"preview", []string{"repair-down", "--version", "100", "--dry-run"}, "preview", 0}, {"repair", []string{"repair-down", "--version", "100", "--yes"}, "repair", 0},
	} {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			r := &runner{confirmed: true}
			output, e := invoke(t, config(r, t.TempDir()), tt.args...)
			if e != nil || output == "" || len(r.actions) != 1 || r.actions[0] != tt.action || r.steps != tt.steps {
				t.Fatalf("action=%v steps=%d output=%q err=%v", r.actions, r.steps, output, e)
			}
		})
	}
}

func TestRejectBeforeResolver(t *testing.T) {
	for _, args := range [][]string{{"up", "--steps", "0"}, {"up", "--steps", "-1"}, {"down", "--steps", "0"}, {"down", "--all", "--steps", "1"}, {"drop"}, {"repair-down", "--version", "1"}, {"repair-down", "--version", "0", "--yes"}, {"repair-down", "--version", "1", "--yes", "--dry-run"}, {"up", "unexpected"}, {"create", "--name", "x", "unexpected"}, {"validate", "unexpected"}} {
		t.Run(args[0]+"_"+args[len(args)-1], func(t *testing.T) {
			t.Parallel()
			calls := 0
			cfg := config(&runner{}, t.TempDir())
			cfg.Resolve = func(context.Context, *cli.Command) (urfavecli.Target, error) { calls++; return urfavecli.Target{}, nil }
			_, e := invoke(t, cfg, args...)
			if e == nil || calls != 0 {
				t.Fatalf("invalid args=%v calls=%d err=%v", args, calls, e)
			}
		})
	}
}

func TestCreateAndValidateDoNotResolveDB(t *testing.T) {
	r := &runner{}
	cfg := config(r, t.TempDir())
	out, e := invoke(t, cfg, "create", "--name", "users", "--version", "100")
	if e != nil || out == "" || len(r.actions) != 0 {
		t.Fatalf("create %q %v", out, e)
	}
	if _, e = invoke(t, cfg, "validate"); !errors.Is(e, x.ErrInvalidSource) {
		t.Fatal(e)
	}
	dir := t.TempDir()
	cfg = config(r, dir)
	out, e = invoke(t, cfg, "validate")
	if e != nil || out == "" || len(r.actions) != 0 {
		t.Fatalf("validate %q %v", out, e)
	}
	cfg.CreateDir = ""
	if _, e = invoke(t, cfg, "create", "--name", "x"); !errors.Is(e, x.ErrInvalidConfig) {
		t.Fatal(e)
	}
	cfg.ResolveSource = func(context.Context, *cli.Command) (x.Source, error) { return x.Source{}, failed }
	if _, e = invoke(t, cfg, "validate"); !errors.Is(e, failed) {
		t.Fatal(e)
	}
}

func TestCleanupAndOperationErrors(t *testing.T) {
	for _, args := range [][]string{{"up"}, {"down"}, {"down", "--all"}, {"status"}, {"drop", "--yes"}, {"repair-down", "--version", "1", "--dry-run"}, {"repair-down", "--version", "1", "--yes"}} {
		t.Run(args[0], func(t *testing.T) {
			r := &runner{err: failed, confirmed: true}
			cfg := config(r, t.TempDir())
			cleaned := 0
			cfg.Resolve = func(context.Context, *cli.Command) (urfavecli.Target, error) {
				return urfavecli.Target{Runner: r, Cleanup: func(ctx context.Context) error {
					if ctx.Err() != nil {
						t.Error("cleanup canceled")
					}
					cleaned++
					return failed
				}}, nil
			}
			out, e := invoke(t, cfg, args...)
			if !errors.Is(e, failed) || cleaned != 1 {
				t.Fatalf("cleanup=%d output=%q err=%v", cleaned, out, e)
			}
			if args[0] == "repair-down" && args[len(args)-1] == "--yes" && out == "" {
				t.Fatal("lost confirmed repair")
			}
		})
	}
	cfg := config(&runner{}, t.TempDir())
	cfg.Resolve = func(context.Context, *cli.Command) (urfavecli.Target, error) { return urfavecli.Target{}, failed }
	if _, e := invoke(t, cfg, "up"); !errors.Is(e, failed) {
		t.Fatal(e)
	}
	cfg.Resolve = func(context.Context, *cli.Command) (urfavecli.Target, error) { return urfavecli.Target{}, nil }
	if _, e := invoke(t, cfg, "up"); !errors.Is(e, x.ErrInvalidConfig) {
		t.Fatal(e)
	}
	r := &runner{err: failed}
	if out, e := invoke(t, config(r, t.TempDir()), "repair-down", "--version", "1", "--yes"); !errors.Is(e, failed) || out != "" {
		t.Fatalf("unconfirmed printed %q %v", out, e)
	}
}

func TestConstructorValidation(t *testing.T) {
	if _, e := urfavecli.Command(urfavecli.Config{}); !errors.Is(e, x.ErrInvalidConfig) {
		t.Fatal(e)
	}
	for _, flags := range [][]cli.Flag{{nil}, {&cli.StringFlag{Name: "dsn", Aliases: []string{"steps"}}}, {&cli.StringFlag{Name: "dsn"}, &cli.StringFlag{Name: "dsn"}}} {
		cfg := config(&runner{}, t.TempDir())
		cfg.Flags = flags
		if _, e := urfavecli.Command(cfg); !errors.Is(e, x.ErrInvalidConfig) {
			t.Fatal(e)
		}
	}
	cfg := config(&runner{}, t.TempDir())
	cfg.EnableDrop = false
	cfg.Name = "db"
	cfg.Flags = []cli.Flag{&cli.StringFlag{Name: "dsn"}}
	c, e := urfavecli.Command(cfg)
	if e != nil || c.Name != "db" {
		t.Fatal(e)
	}
	for _, child := range c.Commands {
		if child.Name == "drop" {
			t.Fatal("disabled drop present")
		}
	}
}

type badWriter struct{}

func (badWriter) Write([]byte) (int, error) { return 0, failed }
func TestOutputError(t *testing.T) {
	c, e := urfavecli.Command(config(&runner{}, t.TempDir()))
	if e != nil {
		t.Fatal(e)
	}
	c.Writer = badWriter{}
	c.ExitErrHandler = func(context.Context, *cli.Command, error) {}
	if e = c.Run(t.Context(), []string{"migrations", "up"}); !errors.Is(e, failed) {
		t.Fatal(e)
	}
}

func TestTypedNilResolverAndDecimalSteps(t *testing.T) {
	cfg := config(&runner{}, t.TempDir())
	cfg.Resolve = func(context.Context, *cli.Command) (urfavecli.Target, error) {
		var r *runner
		return urfavecli.Target{Runner: r}, nil
	}
	if _, e := invoke(t, cfg, "up"); !errors.Is(e, x.ErrInvalidConfig) {
		t.Fatal(e)
	}
	r := &runner{}
	if _, e := invoke(t, config(r, t.TempDir()), "up", "--steps", "009"); e != nil || r.steps != 9 {
		t.Fatalf("decimal version: %d %v", r.steps, e)
	}
}

func TestValidateRequiresAndUsesExplicitDialect(t *testing.T) {
	dir := t.TempDir()
	for name, sql := range map[string]string{"1_x.up.sql": "/* outer /* inner */ SELECT 1; -- */", "1_x.down.sql": "SELECT 1;"} {
		if err := os.WriteFile(dir+"/"+name, []byte(sql), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	r := &runner{}
	cfg := config(r, dir)
	cfg.Dialect = ""
	if _, err := invoke(t, cfg, "validate"); !errors.Is(err, x.ErrInvalidConfig) {
		t.Fatalf("ambiguous source validated: %v", err)
	}
	cfg.Dialect = "sqlite"
	if _, err := invoke(t, cfg, "validate"); err != nil {
		t.Fatal(err)
	}
	if len(r.actions) != 0 {
		t.Fatal("validate opened database")
	}
	cfg.Dialect = ""
	cfg.ResolveSource = func(context.Context, *cli.Command) (x.Source, error) {
		return x.NewSource(os.DirFS(dir), ".", x.WithDialect("sqlite"))
	}
	if _, err := invoke(t, cfg, "validate"); err != nil {
		t.Fatal(err)
	}
	cfg.Dialect = "invalid"
	if _, err := urfavecli.Command(cfg); !errors.Is(err, x.ErrInvalidConfig) {
		t.Fatal(err)
	}
}
