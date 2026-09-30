package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime/debug"
	"strings"
	"testing"
)

func TestExecutable(t *testing.T) {
	if testing.Short() {
		t.Skip("instrumented executable build")
	}
	dir := t.TempDir()
	binary := filepath.Join(dir, "xmigrator")
	if os.Getenv("GOOS") == "windows" || strings.HasSuffix(os.Args[0], ".exe") {
		binary += ".exe"
	}
	build := exec.CommandContext(t.Context(), "go", "build", "-cover", "-covermode=atomic", "-coverpkg=github.com/sxwebdev/xmigrator/cmd/xmigrator", "-o", binary, ".")
	if out, e := build.CombinedOutput(); e != nil {
		t.Fatalf("build: %v\n%s", e, out)
	}
	coverageDir := os.Getenv("XMIGRATOR_BINARY_COVER_DIR")
	if coverageDir == "" {
		coverageDir = filepath.Join(dir, "coverage")
	}
	if e := os.MkdirAll(coverageDir, 0o700); e != nil {
		t.Fatal(e)
	}
	for _, tt := range []struct {
		name    string
		args    []string
		success bool
	}{{"help", []string{"--help"}, true}, {"version", []string{"--version"}, true}, {"invalid", []string{"up", "--steps", "0"}, false}} {
		t.Run(tt.name, func(t *testing.T) {
			cmd := exec.CommandContext(t.Context(), binary, tt.args...)
			cmd.Env = append(os.Environ(), "GOCOVERDIR="+coverageDir)
			output, e := cmd.CombinedOutput()
			if (e == nil) != tt.success || len(output) == 0 {
				t.Fatalf("executable: %v\n%s", e, output)
			}
		})
	}
	for _, tt := range []struct {
		name, initial, current         string
		removeDown, removeAll, applied bool
		command, diagnostic            string
	}{
		{name: "checksum", initial: "SELECT 1;", current: "SELECT 2;", applied: true, command: "up", diagnostic: "migration 42"},
		{name: "unsafe", current: "BEGIN;", command: "up", diagnostic: "migration 42"},
		{name: "unsafe_validate", current: "BEGIN;", command: "validate", diagnostic: "migration 42"},
		{name: "missing_pair", current: "SELECT 1;", removeDown: true, command: "validate", diagnostic: "42_users"},
		{name: "unknown", initial: "SELECT 1;", removeAll: true, applied: true, command: "up", diagnostic: "migration 42"},
		{name: "missing_table", current: "SELECT * FROM absent_table;", command: "up", diagnostic: "table does not exist"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			migrations := t.TempDir()
			db := filepath.Join(t.TempDir(), "test.db")
			up, down := filepath.Join(migrations, "42_users.up.sql"), filepath.Join(migrations, "42_users.down.sql")
			write := func(path, sql string) {
				t.Helper()
				if err := os.WriteFile(path, []byte(sql), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			write(up, tt.initial)
			write(down, "SELECT 1;")
			execute := func(operation string) ([]byte, error) {
				t.Helper()
				cmd := exec.CommandContext(t.Context(), binary, operation, "--driver", "sqlite", "--dsn", db, "--path", migrations, "--unknown-applied", "error")
				cmd.Env = append(os.Environ(), "GOCOVERDIR="+coverageDir)
				return cmd.CombinedOutput()
			}
			if tt.applied {
				if output, err := execute("up"); err != nil {
					t.Fatalf("setup: %v %s", err, output)
				}
			}
			write(up, tt.current)
			if tt.removeDown || tt.removeAll {
				if err := os.Remove(down); err != nil {
					t.Fatal(err)
				}
			}
			if tt.removeAll {
				if err := os.Remove(up); err != nil {
					t.Fatal(err)
				}
			}
			output, err := execute(tt.command)
			if err == nil || !strings.Contains(string(output), tt.diagnostic) {
				t.Fatalf("missing executable diagnostic %q: %v %s", tt.diagnostic, err, output)
			}
		})
	}
}

func TestBuildVersion(t *testing.T) {
	for _, tt := range []struct {
		name, override, module, want string
		valid                        bool
	}{
		{"override", "custom", "v0.1.0", "custom", true}, {"module", "", "v0.1.0", "v0.1.0", true}, {"development", "", "(devel)", "dev", true}, {"empty", "", "", "dev", true}, {"missing_info", "", "", "dev", false},
	} {
		t.Run(tt.name, func(t *testing.T) {
			got := binaryVersion(tt.override, func() (*debug.BuildInfo, bool) {
				return &debug.BuildInfo{Main: debug.Module{Version: tt.module}}, tt.valid
			})
			if got != tt.want {
				t.Fatalf("version=%s want=%s", got, tt.want)
			}
		})
	}
}
