package app

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"os"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgconn"
	x "github.com/sxwebdev/xmigrator"
	"github.com/sxwebdev/xmigrator/cli/urfavecli"
	pgdriver "github.com/sxwebdev/xmigrator/driver/pgx"
	cli "github.com/urfave/cli/v3"
)

func TestResolverConstructorFailures(t *testing.T) {
	fault := errors.New("constructor failure")
	for _, stage := range []string{"source", "open", "nil_pg_driver", "command"} {
		t.Run(stage, func(t *testing.T) {
			deps := defaults()
			want := fault
			driver := "sqlite"
			switch stage {
			case "source":
				deps.source = func(context.Context, *cli.Command) (x.Source, error) { return x.Source{}, fault }
			case "open":
				deps.open = func(string, string) (*sql.DB, error) { return nil, fault }
			case "nil_pg_driver":
				driver = "pgx"
				want = x.ErrInvalidConfig
				deps.pg = func(string, pgdriver.Config) (*pgdriver.Driver, error) { return nil, nil }
			case "command":
				deps.command = func(urfavecli.Config) (*cli.Command, error) { return nil, fault }
			}
			var out bytes.Buffer
			e := run(t.Context(), []string{"xmigrator", "up", "--driver", driver, "--dsn", "unused", "--timeout", "1s"}, &out, &out, "test", deps)
			if !errors.Is(e, want) {
				t.Fatal(e)
			}
		})
	}
}

func TestSafeErrors(t *testing.T) {
	for _, err := range []error{x.ErrCommitOutcomeUnknown, x.ErrChecksumMismatch, x.ErrDefinitionMismatch, x.ErrHookMismatch, x.ErrIrreversible, x.ErrUnknownApplied, x.ErrMetadataConflict, x.ErrMetadataVersion, x.ErrHistoryConflict, x.ErrInvalidSource, x.ErrInvalidSteps, x.ErrVersionExists, x.ErrInvalidConfig, x.ErrNotApplied, x.ErrUnsafeSQL, x.ErrDropScope, x.ErrForeignKey, context.Canceled, context.DeadlineExceeded, &pgconn.PgError{Code: "42P01", Message: "secret-password"}, errors.New("secret-password")} {
		message := ErrorMessage(err)
		if message == "" || bytes.Contains([]byte(message), []byte("secret-password")) {
			t.Fatal(message)
		}
	}
}

func TestSafeErrorsIdentifyMigrationAndSource(t *testing.T) {
	for _, tt := range []struct {
		name     string
		err      error
		contains []string
	}{
		{"migration", &x.MigrationError{Version: 42, Name: "create_users", Direction: x.Up, Err: &pgconn.PgError{Code: "42P01", Message: "secret-password"}}, []string{"42", "create_users", "up", "42P01"}},
		{"source", &x.SourceError{File: "42_users.up.sql", Err: x.ErrInvalidSource}, []string{"42_users.up.sql", "invalid SQL migration source"}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			message := ErrorMessage(tt.err)
			for _, value := range tt.contains {
				if !strings.Contains(message, value) {
					t.Fatalf("diagnostic lost %q: %s", value, message)
				}
			}
			if strings.Contains(message, "secret-password") {
				t.Fatal("backend payload leaked")
			}
		})
	}
}

func TestUnknownDriverWithCustomSource(t *testing.T) {
	deps := defaults()
	deps.source = func(context.Context, *cli.Command) (x.Source, error) { return x.NewSource(os.DirFS(t.TempDir()), ".") }
	var out bytes.Buffer
	if err := run(t.Context(), []string{"xmigrator", "up", "--driver", "unsupported", "--dsn", "unused"}, &out, &out, "test", deps); !errors.Is(err, x.ErrInvalidConfig) {
		t.Fatal(err)
	}
}

func TestSafeSQLiteBackendDiagnostic(t *testing.T) {
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	_, err = db.ExecContext(t.Context(), "SELECT * FROM missing_table")
	message := ErrorMessage(err)
	if !strings.Contains(message, "SQLite code 1") || strings.Contains(message, "SELECT") || strings.Contains(message, "missing_table") {
		t.Fatalf("unhelpful or unsafe SQLite error: %s", message)
	}
}

func TestSafeSQLiteFailureReasons(t *testing.T) {
	for _, tt := range []struct{ name, sql, reason string }{
		{"missing_table", "SELECT * FROM missing_table", "table does not exist"},
		{"missing_column", "SELECT absent FROM users", "column does not exist"},
		{"syntax", "SELEC 1", "SQL syntax error"},
		{"existing_object", "CREATE TABLE users(id INTEGER)", "database object already exists"},
		{"unique", "INSERT INTO users VALUES(1,'x',1,NULL)", "unique constraint violation"},
		{"not_null", "INSERT INTO users VALUES(2,NULL,1,NULL)", "not-null constraint violation"},
		{"foreign_key", "INSERT INTO users VALUES(2,'x',1,999)", "foreign-key constraint violation"},
		{"check", "INSERT INTO users VALUES(2,'x',0,NULL)", "check constraint violation"},
		{"fallback", "SELECT missing_function()", "database rejected operation"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			db, err := sql.Open("sqlite", ":memory:")
			if err != nil {
				t.Fatal(err)
			}
			db.SetMaxOpenConns(1)
			t.Cleanup(func() { db.Close() })
			if _, err := db.ExecContext(t.Context(), "PRAGMA foreign_keys=ON;CREATE TABLE parent(id INTEGER PRIMARY KEY);CREATE TABLE users(id INTEGER UNIQUE, required TEXT NOT NULL, checked INTEGER CHECK(checked>0), parent_id INTEGER REFERENCES parent(id));INSERT INTO users VALUES(1,'present',1,NULL);"); err != nil {
				t.Fatal(err)
			}
			_, err = db.ExecContext(t.Context(), tt.sql)
			if err == nil {
				t.Fatal("failure fixture succeeded")
			}
			message := ErrorMessage(err)
			if !strings.Contains(message, tt.reason) || !strings.Contains(message, "SQLite code") || strings.Contains(message, tt.sql) {
				t.Fatalf("failure reason lost or SQL leaked: %s", message)
			}
		})
	}
}
