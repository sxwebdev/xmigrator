package integration_test

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"testing/fstest"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
	x "github.com/sxwebdev/xmigrator"
	pgdriver "github.com/sxwebdev/xmigrator/driver/pgx"
	sqdriver "github.com/sxwebdev/xmigrator/driver/sqlite"
	"github.com/sxwebdev/xmigrator/internal/contracttest"
)

func reviewSQLite(t *testing.T) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", filepath.Join(t.TempDir(), "review.db"))
	if err != nil {
		t.Fatal(err)
	}
	db.SetMaxOpenConns(1)
	t.Cleanup(func() {
		if err := db.Close(); err != nil {
			t.Error(err)
		}
	})
	return db
}

func TestSQLiteLegacyOrphanDoesNotBlockUnrelatedMigration(t *testing.T) {
	db := reviewSQLite(t)
	if _, err := db.ExecContext(t.Context(), "PRAGMA foreign_keys=OFF;CREATE TABLE parent(id INTEGER PRIMARY KEY);CREATE TABLE child(id INTEGER REFERENCES parent(id));INSERT INTO child VALUES(99);"); err != nil {
		t.Fatal(err)
	}
	driver, err := sqdriver.FromDB(db, sqdriver.Config{})
	if err != nil {
		t.Fatal(err)
	}
	files := fstest.MapFS{}
	contracttest.Pair(files, 1, "CREATE TABLE added(id INTEGER);", "DROP TABLE added;")
	source, _ := x.NewSource(files, ".")
	runner, err := x.New(source, driver)
	if err != nil {
		t.Fatal(err)
	}
	result, err := runner.Up(t.Context(), 0)
	if err != nil || len(result.Actions) != 1 {
		t.Fatalf("unrelated migration rejected: %+v %v", result, err)
	}
	var n int
	if err := db.QueryRowContext(t.Context(), "SELECT count(*) FROM child WHERE id=99").Scan(&n); err != nil || n != 1 {
		t.Fatalf("legacy data changed: %d %v", n, err)
	}
	contracttest.Pair(files, 2, "INSERT INTO child VALUES(100);", "DELETE FROM child WHERE id=100;")
	if _, err := runner.Up(t.Context(), 0); err == nil {
		t.Fatal("new FK violation accepted")
	}
	if err := db.QueryRowContext(t.Context(), "SELECT count(*) FROM child").Scan(&n); err != nil || n != 1 {
		t.Fatalf("failed migration left effects: %d %v", n, err)
	}
}

func TestSQLiteRepairDoesNotBootstrap(t *testing.T) {
	db := reviewSQLite(t)
	driver, err := sqdriver.FromDB(db, sqdriver.Config{})
	if err != nil {
		t.Fatal(err)
	}
	files := fstest.MapFS{}
	contracttest.Pair(files, 1, "SELECT 1;", "SELECT 1;")
	source, _ := x.NewSource(files, ".")
	runner, err := x.New(source, driver)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := runner.RepairDown(t.Context(), 1); !errors.Is(err, x.ErrNotApplied) {
		t.Fatal(err)
	}
	var n int
	if err := db.QueryRowContext(t.Context(), "SELECT count(*) FROM sqlite_schema WHERE name LIKE '__xmigrator_%'").Scan(&n); err != nil || n != 0 {
		t.Fatalf("repair created metadata: %d %v", n, err)
	}
}

func TestSQLiteSourceUsesSQLiteLexicalRules(t *testing.T) {
	for _, tt := range []struct{ name, sql string }{
		{"nonnested_comment", "/* outer /* inner */ CREATE TABLE added(id INTEGER); -- */"},
		{"carriage_return_comment", "CREATE TABLE added(id INTEGER);\n-- ignored\r-- xmigrator:hooks=ignored\n"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			db := reviewSQLite(t)
			driver, err := sqdriver.FromDB(db, sqdriver.Config{})
			if err != nil {
				t.Fatal(err)
			}
			files := fstest.MapFS{}
			contracttest.Pair(files, 1, tt.sql, "DROP TABLE added;")
			source, _ := x.NewSource(files, ".")
			runner, err := x.New(source, driver)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := runner.Up(t.Context(), 0); err != nil {
				t.Fatal(err)
			}
			var n int
			if err := db.QueryRowContext(t.Context(), "SELECT count(*) FROM sqlite_schema WHERE name='added'").Scan(&n); err != nil || n != 1 {
				t.Fatalf("table not created: %d %v", n, err)
			}
		})
	}
}

func TestSQLiteInheritedBusyTimeoutAndDeadline(t *testing.T) {
	db := reviewSQLite(t)
	if _, err := db.ExecContext(t.Context(), "PRAGMA busy_timeout=5000"); err != nil {
		t.Fatal(err)
	}
	driver, err := sqdriver.FromDB(db, sqdriver.Config{})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), time.Second)
	defer cancel()
	err = driver.WithSession(ctx, func(session x.Session[sqdriver.Tx]) error {
		_, err := session.InTx(ctx, x.Script{Kind: x.DownNoop}, func(tx x.Transaction[sqdriver.Tx]) error {
			var n int
			if err := tx.Executor().QueryRowContext(ctx, "PRAGMA busy_timeout").Scan(&n); err != nil {
				return err
			}
			if n <= 0 || n > 1000 {
				t.Fatalf("timeout not bounded by deadline: %d", n)
			}
			return nil
		})
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	var n int
	if err := db.QueryRowContext(t.Context(), "PRAGMA busy_timeout").Scan(&n); err != nil || n != 5000 {
		t.Fatalf("timeout not restored: %d %v", n, err)
	}
}

func reviewPG(t *testing.T) *pgxpool.Pool {
	t.Helper()
	dsn := os.Getenv("XMIGRATOR_TEST_PG_DSN")
	if dsn == "" {
		if testing.Short() {
			t.Skip("PostgreSQL unavailable in short mode")
		}
		t.Fatal("XMIGRATOR_TEST_PG_DSN required")
	}
	pool, err := pgxpool.New(t.Context(), dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	return pool
}

func TestPGServerCommitRejectionIsKnownRollback(t *testing.T) {
	b := pgBackend(t)
	files := fstest.MapFS{}
	contracttest.Pair(files, 1, "CREATE TABLE parent(id INT PRIMARY KEY);CREATE TABLE child(id INT REFERENCES parent(id) DEFERRABLE INITIALLY DEFERRED);INSERT INTO child VALUES(99);", "DROP TABLE child;DROP TABLE parent;")
	source, _ := x.NewSource(files, ".")
	runner, err := x.New(source, b.Driver)
	if err != nil {
		t.Fatal(err)
	}
	result, err := runner.Up(t.Context(), 0)
	pgerr, ok := errors.AsType[*pgconn.PgError](err)
	if !ok || pgerr.Code != "23503" || pgerr.SeverityUnlocalized != "ERROR" || errors.Is(err, x.ErrCommitOutcomeUnknown) || len(result.Actions) != 0 {
		t.Fatalf("known server rejection misclassified: %+v %v", result, err)
	}
	t.Logf("commit rejection severity=%q, unlocalized=%q", pgerr.Severity, pgerr.SeverityUnlocalized)
	n, err := b.Count(t.Context(), "SELECT count(*) FROM information_schema.tables WHERE table_schema=current_schema() AND table_name IN ('parent','child')")
	if err != nil || n != 0 {
		t.Fatalf("rejected commit left DDL: %d %v", n, err)
	}
}

func TestPGInheritedTimeoutsAndTransactionalSet(t *testing.T) {
	pool := reviewPG(t)
	config := pool.Config().ConnConfig.Copy()
	config.RuntimeParams["lock_timeout"] = "3s"
	config.RuntimeParams["statement_timeout"] = "4s"
	driver, err := pgdriver.FromConfig(config, pgdriver.Config{})
	if err != nil {
		t.Fatal(err)
	}
	err = driver.WithSession(t.Context(), func(session x.Session[pgx.Tx]) error {
		_, err := session.InTx(t.Context(), x.Script{Kind: x.DownNoop}, func(tx x.Transaction[pgx.Tx]) error {
			var lock, statement string
			if err := tx.Executor().QueryRow(t.Context(), "SELECT current_setting('lock_timeout'),current_setting('statement_timeout')").Scan(&lock, &statement); err != nil {
				return err
			}
			if lock != "3s" || statement != "4s" {
				t.Fatalf("inherited timeouts overwritten: %s %s", lock, statement)
			}
			return tx.ExecScript(t.Context(), x.Script{SQL: "SET LOCAL lock_timeout='2s'; SET CONSTRAINTS ALL DEFERRED;"})
		})
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
}

func TestPGExistingEmptySchemaWithoutDatabaseCreate(t *testing.T) {
	pool := reviewPG(t)
	suffix := fmt.Sprintf("%d", time.Now().UnixNano())
	role, schema := "review_role_"+suffix, "review_meta_"+suffix
	if _, err := pool.Exec(t.Context(), "CREATE ROLE "+role+";CREATE SCHEMA "+schema+" AUTHORIZATION "+role); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.WithoutCancel(t.Context()), 5*time.Second)
		defer cancel()
		if _, err := pool.Exec(ctx, "DROP SCHEMA "+schema+" CASCADE;DROP ROLE "+role); err != nil {
			t.Error(err)
		}
	})
	config := pool.Config().ConnConfig.Copy()
	config.RuntimeParams["role"] = role
	driver, err := pgdriver.FromConfig(config, pgdriver.Config{MetadataSchema: schema})
	if err != nil {
		t.Fatal(err)
	}
	if err := driver.WithSession(t.Context(), func(session x.Session[pgx.Tx]) error { return session.EnsureMetadata(t.Context()) }); err != nil {
		t.Fatal(err)
	}
	var n int
	if err := pool.QueryRow(t.Context(), "SELECT count(*) FROM information_schema.tables WHERE table_schema=$1", schema).Scan(&n); err != nil || n != 2 {
		t.Fatalf("metadata missing: %d %v", n, err)
	}
}

func TestPGRepairDoesNotBootstrap(t *testing.T) {
	b := pgBackend(t)
	files := fstest.MapFS{}
	contracttest.Pair(files, 1, "SELECT 1;", "SELECT 1;")
	source, _ := x.NewSource(files, ".")
	runner, err := x.New(source, b.Driver)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := runner.RepairDown(t.Context(), 1); !errors.Is(err, x.ErrNotApplied) {
		t.Fatal(err)
	}
	n, err := b.Count(t.Context(), "SELECT count(*) FROM pg_catalog.pg_namespace WHERE nspname='meta_' || current_schema()")
	// The runner must still report no applied history after a failed repair.
	status, statusErr := runner.Status(t.Context())
	if err != nil || n != 0 || statusErr != nil || len(status.Migrations) != 1 || status.Migrations[0].State != "pending" {
		t.Fatalf("repair changed clean database: %+v %v %v", status, err, statusErr)
	}
}

func TestSQLiteDeferredCommitFailureIsKnownRollback(t *testing.T) {
	db := reviewSQLite(t)
	driver, err := sqdriver.FromDB(db, sqdriver.Config{})
	if err != nil {
		t.Fatal(err)
	}
	files := fstest.MapFS{}
	contracttest.Pair(files, 1, "CREATE TABLE parent(id INTEGER PRIMARY KEY);CREATE TABLE child(id INTEGER REFERENCES parent(id) DEFERRABLE INITIALLY DEFERRED);INSERT INTO child VALUES(99);", "DROP TABLE child;DROP TABLE parent;")
	source, _ := x.NewSource(files, ".")
	runner, err := x.New(source, driver)
	if err != nil {
		t.Fatal(err)
	}
	result, err := runner.Up(t.Context(), 0)
	if err == nil || errors.Is(err, x.ErrCommitOutcomeUnknown) || len(result.Actions) != 0 {
		t.Fatalf("confirmed rollback misclassified: %+v %v", result, err)
	}
	var n int
	if err := db.QueryRowContext(t.Context(), "SELECT count(*) FROM sqlite_schema WHERE name IN ('child','parent')").Scan(&n); err != nil || n != 0 {
		t.Fatalf("failed commit left DDL: %d %v", n, err)
	}
}

func TestPGConcurrentRefreshRollsBack(t *testing.T) {
	b := pgBackend(t)
	sourceFiles := fstest.MapFS{}
	contracttest.Pair(sourceFiles, 1, "CREATE TABLE entries(id INT PRIMARY KEY);INSERT INTO entries VALUES(1);CREATE MATERIALIZED VIEW snapshot AS SELECT id FROM entries;CREATE UNIQUE INDEX snapshot_id ON snapshot(id);", "DROP MATERIALIZED VIEW snapshot;DROP TABLE entries;")
	source, _ := x.NewSource(sourceFiles, ".")
	runner, err := x.New(source, b.Driver)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := runner.Up(t.Context(), 0); err != nil {
		t.Fatal(err)
	}
	err = b.Driver.WithSession(t.Context(), func(session x.Session[pgx.Tx]) error {
		_, err := session.InTx(t.Context(), x.Script{Kind: x.DownNoop}, func(tx x.Transaction[pgx.Tx]) error {
			if err := tx.ExecScript(t.Context(), x.Script{SQL: "INSERT INTO entries VALUES(2);REFRESH MATERIALIZED VIEW CONCURRENTLY snapshot;"}); err != nil {
				return err
			}
			var n int
			if err := tx.Executor().QueryRow(t.Context(), "SELECT count(*) FROM snapshot").Scan(&n); err != nil {
				return err
			}
			if n != 2 {
				t.Fatalf("refresh did not execute: %d", n)
			}
			return x.ErrHistoryConflict
		})
		return err
	})
	if !errors.Is(err, x.ErrHistoryConflict) {
		t.Fatal(err)
	}
	n, err := b.Count(t.Context(), "SELECT count(*) FROM snapshot")
	if err != nil || n != 1 {
		t.Fatalf("concurrent refresh escaped rollback: %d %v", n, err)
	}
}
