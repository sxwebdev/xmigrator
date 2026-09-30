package app_test

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	x "github.com/sxwebdev/xmigrator"
	"github.com/sxwebdev/xmigrator/cmd/xmigrator/internal/app"
)

func run(t *testing.T, args ...string) (string, error) {
	t.Helper()
	var out, logs bytes.Buffer
	e := app.Run(t.Context(), append([]string{"xmigrator"}, args...), &out, &logs, "test")
	if bytes.Contains(logs.Bytes(), []byte("SELECT")) {
		t.Fatal("SQL leaked into logger")
	}
	return out.String(), e
}

func TestSQLiteCLI(t *testing.T) {
	dir := t.TempDir()
	dbfile := filepath.Join(t.TempDir(), "app.db")
	for name, content := range map[string]string{"1_users.up.sql": "CREATE TABLE users(id INTEGER);INSERT INTO users VALUES(42);", "1_users.down.sql": "DROP TABLE users;"} {
		if e := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o600); e != nil {
			t.Fatal(e)
		}
	}
	flags := []string{"--driver", "sqlite", "--dsn", dbfile, "--path", dir}
	for _, op := range [][]string{{"status"}, {"up", "--steps", "1"}, {"status"}, {"repair-down", "--version", "1", "--dry-run"}, {"repair-down", "--version", "1", "--yes"}, {"down", "--all"}, {"up"}, {"drop", "--yes"}} {
		args := append(append([]string{}, op...), flags...)
		out, e := run(t, args...)
		if e != nil || out == "" {
			t.Fatalf("%v output=%q err=%v", op, out, e)
		}
	}
	db, e := sql.Open("sqlite", dbfile)
	if e != nil {
		t.Fatal(e)
	}
	t.Cleanup(func() { db.Close() })
	var n int
	if e = db.QueryRowContext(t.Context(), "SELECT count(*) FROM sqlite_schema WHERE name='users'").Scan(&n); e != nil || n != 0 {
		t.Fatalf("drop %d %v", n, e)
	}
	if _, e = run(t, "validate", "--path", dir); e != nil {
		t.Fatal(e)
	}
	newdir := t.TempDir()
	if _, e = run(t, "create", "--name", "new_table", "--version", "100", "--path", newdir); e != nil {
		t.Fatal(e)
	}
}

func TestHelpVersionAndValidation(t *testing.T) {
	for _, args := range [][]string{{"--help"}, {"--version"}, {"up", "--help"}} {
		out, e := run(t, args...)
		if e != nil || out == "" {
			t.Fatalf("%q %v", out, e)
		}
	}
	for _, args := range [][]string{
		{"up", "--checksum-policy", "other", "--dsn", "ignored"}, {"up", "--unknown-applied", "other", "--dsn", "ignored"}, {"up", "--timeout", "-1s"}, {"up"}, {"up", "--driver", "other", "--dsn", "ignored"}, {"up", "--driver", "pgx", "--busy-timeout", "1s", "--dsn", "ignored"}, {"up", "--driver", "sqlite", "--metadata-schema", "app", "--dsn", "ignored"}, {"up", "--driver", "sqlite", "--metadata-prefix", "BAD", "--dsn", "ignored"}, {"up", "--driver", "pgx", "--metadata-schema", "BAD", "--dsn", "postgres://localhost/test"},
	} {
		t.Run(args[len(args)-1], func(t *testing.T) {
			if _, e := run(t, args...); !errors.Is(e, x.ErrInvalidConfig) {
				t.Fatalf("args=%v error=%v", args, e)
			}
		})
	}
}

func TestEnvironmentAndPolicy(t *testing.T) {
	dir := t.TempDir()
	dbfile := filepath.Join(t.TempDir(), "env.db")
	t.Setenv("XMIGRATOR_DSN", dbfile)
	if e := os.WriteFile(filepath.Join(dir, "1_x.up.sql"), []byte("SELECT 1;"), 0o600); e != nil {
		t.Fatal(e)
	}
	if e := os.WriteFile(filepath.Join(dir, "1_x.down.sql"), []byte("SELECT 2;"), 0o600); e != nil {
		t.Fatal(e)
	}
	if _, e := run(t, "up", "--driver", "sqlite", "--path", dir, "--timeout", "5s", "--unknown-applied", "error"); e != nil {
		t.Fatal(e)
	}
	if e := os.WriteFile(filepath.Join(dir, "1_x.up.sql"), []byte("SELECT 3;"), 0o600); e != nil {
		t.Fatal(e)
	}
	if _, e := run(t, "up", "--driver", "sqlite", "--path", dir); !errors.Is(e, x.ErrChecksumMismatch) {
		t.Fatal(e)
	}
	for _, p := range []string{"warn", "disabled"} {
		if _, e := run(t, "up", "--driver", "sqlite", "--path", dir, "--checksum-policy", p); e != nil {
			t.Fatal(e)
		}
	}
}

func TestPGCLI(t *testing.T) {
	dsn := os.Getenv("XMIGRATOR_TEST_PG_DSN")
	if dsn == "" {
		if testing.Short() {
			t.Skip("PostgreSQL integration")
		}
		t.Fatal("XMIGRATOR_TEST_PG_DSN required")
	}
	conn, e := pgx.Connect(t.Context(), dsn)
	if e != nil {
		t.Fatal(e)
	}
	schema := fmt.Sprintf("cli_%d", time.Now().UnixNano())
	meta := "meta_" + schema
	_, e = conn.Exec(t.Context(), "CREATE SCHEMA "+schema)
	if e != nil {
		t.Fatal(e)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.WithoutCancel(t.Context()), 5*time.Second)
		defer cancel()
		_, e := conn.Exec(ctx, "DROP SCHEMA IF EXISTS "+schema+" CASCADE;DROP SCHEMA IF EXISTS "+meta+" CASCADE;")
		if e != nil {
			t.Error(e)
		}
		conn.Close(ctx)
	})
	dir := t.TempDir()
	for name, sql := range map[string]string{"1_x.up.sql": "CREATE TABLE " + schema + ".entries(id INTEGER);", "1_x.down.sql": "DROP TABLE " + schema + ".entries;"} {
		if e := os.WriteFile(filepath.Join(dir, name), []byte(sql), 0o600); e != nil {
			t.Fatal(e)
		}
	}
	flags := []string{"--driver", "pgx", "--dsn", dsn, "--path", dir, "--metadata-schema", meta, "--schema", schema, "--timeout", "5s"}
	for _, op := range [][]string{{"up"}, {"status"}, {"repair-down", "--version", "1", "--dry-run"}, {"repair-down", "--version", "1", "--yes"}, {"down", "--steps", "1"}, {"up"}, {"drop", "--yes"}} {
		out, e := run(t, append(append([]string{}, op...), flags...)...)
		if e != nil || out == "" {
			t.Fatalf("PG command %v output=%q error=%v", op, out, e)
		}
	}
}
