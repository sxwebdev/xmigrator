package integration_test

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"testing"
	"testing/fstest"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	x "github.com/sxwebdev/xmigrator"
	pgdriver "github.com/sxwebdev/xmigrator/driver/pgx"
	sqdriver "github.com/sxwebdev/xmigrator/driver/sqlite"
	"github.com/sxwebdev/xmigrator/internal/contracttest"
	_ "modernc.org/sqlite"
)

func sqliteBackend(t *testing.T) contracttest.Backend[sqdriver.Tx] {
	t.Helper()
	db, e := sql.Open("sqlite", filepath.Join(t.TempDir(), "test.db"))
	if e != nil {
		t.Fatal(e)
	}
	t.Cleanup(func() {
		if e := db.Close(); e != nil {
			t.Error(e)
		}
	})
	d, e := sqdriver.FromDB(db, sqdriver.Config{BusyTimeout: time.Second})
	if e != nil {
		t.Fatal(e)
	}
	return contracttest.Backend[sqdriver.Tx]{Driver: d, Count: func(c context.Context, q string) (int, error) {
		var n int
		e := db.QueryRowContext(c, q).Scan(&n)
		return n, e
	}, Execute: func(c context.Context, tx sqdriver.Tx, q string) error { _, e := tx.ExecContext(c, q); return e }}
}
func TestSQLiteContract(t *testing.T) { contracttest.Run(t, sqliteBackend) }
func pgBackend(t *testing.T) contracttest.Backend[pgx.Tx] {
	t.Helper()
	dsn := os.Getenv("XMIGRATOR_TEST_PG_DSN")
	if dsn == "" {
		if testing.Short() {
			t.Skip("PostgreSQL integration disabled in short mode")
		}
		t.Fatal("XMIGRATOR_TEST_PG_DSN must be set for full integration")
	}
	config, e := pgxpool.ParseConfig(dsn)
	if e != nil {
		t.Fatal(e)
	}
	schema := fmt.Sprintf("app_%d", time.Now().UnixNano())
	meta := "meta_" + schema
	config.ConnConfig.RuntimeParams["search_path"] = schema
	pool, e := pgxpool.NewWithConfig(t.Context(), config)
	if e != nil {
		t.Fatal(e)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.WithoutCancel(t.Context()), 5*time.Second)
		defer cancel()
		_, e := pool.Exec(ctx, "DROP SCHEMA IF EXISTS "+schema+" CASCADE;DROP SCHEMA IF EXISTS "+meta+" CASCADE;")
		if e != nil {
			t.Error(e)
		}
		pool.Close()
	})
	if _, e = pool.Exec(t.Context(), "CREATE SCHEMA "+schema); e != nil {
		t.Fatal(e)
	}
	d, e := pgdriver.FromPool(pool, pgdriver.Config{MetadataSchema: meta, DropSchemas: []string{schema}, LockWaitTimeout: time.Second})
	if e != nil {
		t.Fatal(e)
	}
	return contracttest.Backend[pgx.Tx]{Driver: d, Count: func(c context.Context, q string) (int, error) {
		var n int
		e := pool.QueryRow(c, q).Scan(&n)
		return n, e
	}, Execute: func(c context.Context, tx pgx.Tx, q string) error { _, e := tx.Exec(c, q); return e }}
}
func TestPGContract(t *testing.T) { contracttest.Run(t, pgBackend) }
func TestSQLiteForeignKeyRebuild(t *testing.T) {
	b := sqliteBackend(t)
	files := fstest.MapFS{}
	contracttest.Pair(files, 1, "CREATE TABLE parent(id INTEGER PRIMARY KEY);CREATE TABLE child(id INTEGER REFERENCES parent(id) ON DELETE CASCADE);INSERT INTO parent VALUES(1);INSERT INTO child VALUES(1);", "DROP TABLE child;DROP TABLE parent;")
	contracttest.Pair(files, 2, "-- xmigrator:sqlite-foreign-keys=off\nCREATE TABLE replacement(id INTEGER PRIMARY KEY);INSERT INTO replacement SELECT id FROM parent;DROP TABLE parent;ALTER TABLE replacement RENAME TO parent;", "-- xmigrator:noop\n")
	s, _ := x.NewSource(files, ".")
	m, e := x.New(s, b.Driver)
	if e != nil {
		t.Fatal(e)
	}
	if _, e = m.Up(t.Context(), 0); e != nil {
		t.Fatal(e)
	}
	n, e := b.Count(t.Context(), "SELECT count(*) FROM child WHERE id=1")
	if e != nil || n != 1 {
		t.Fatalf("child lost during parent rebuild: %d %v", n, e)
	}
	contracttest.Pair(files, 3, "-- xmigrator:sqlite-foreign-keys=off\nINSERT INTO child VALUES(999);", "DELETE FROM child WHERE id=999;")
	if _, e = m.Up(t.Context(), 0); !errors.Is(e, x.ErrForeignKey) {
		t.Fatalf("FK violation must rollback: %v", e)
	}
	n, e = b.Count(t.Context(), "SELECT count(*) FROM child")
	if e != nil || n != 1 {
		t.Fatalf("FK failure wasn't rolled back: %d %v", n, e)
	}
}

func TestSQLBodies(t *testing.T) {
	t.Run("SQLite_trigger", func(t *testing.T) {
		b := sqliteBackend(t)
		files := fstest.MapFS{}
		contracttest.Pair(files, 1, "CREATE TABLE entries(id INTEGER);CREATE TABLE audit(id INTEGER);CREATE TRIGGER audit_insert AFTER INSERT ON entries BEGIN INSERT INTO audit VALUES(CASE WHEN NEW.id>0 THEN NEW.id ELSE 0 END); END;INSERT INTO entries VALUES(7);", "DROP TABLE entries;DROP TABLE audit;")
		s, _ := x.NewSource(files, ".")
		m, _ := x.New(s, b.Driver)
		if _, e := m.Up(t.Context(), 0); e != nil {
			t.Fatal(e)
		}
		n, e := b.Count(t.Context(), "SELECT count(*) FROM audit WHERE id=7")
		if e != nil || n != 1 {
			t.Fatalf("trigger effect %d %v", n, e)
		}
	})
	t.Run("PostgreSQL_atomic_function", func(t *testing.T) {
		b := pgBackend(t)
		files := fstest.MapFS{}
		contracttest.Pair(files, 1, "CREATE FUNCTION answer() RETURNS integer LANGUAGE SQL BEGIN ATOMIC SELECT CASE WHEN true THEN 42 ELSE 0 END; END;CREATE TABLE entries(id INTEGER);INSERT INTO entries SELECT answer();", "DROP TABLE entries;DROP FUNCTION answer();")
		s, _ := x.NewSource(files, ".")
		m, _ := x.New(s, b.Driver)
		if _, e := m.Up(t.Context(), 0); e != nil {
			t.Fatal(e)
		}
		n, e := b.Count(t.Context(), "SELECT count(*) FROM entries WHERE id=42")
		if e != nil || n != 1 {
			t.Fatalf("function effect %d %v", n, e)
		}
	})
}

func TestSQLiteDialectControlRollbacks(t *testing.T) {
	for _, sql := range []string{
		"CREATE TABLE a$tag$(id INTEGER);COMMIT;CREATE TABLE a$tag$x(id INTEGER);",
		"CREATE TABLE entries(id INTEGER);/* outer /* inner */ COMMIT; -- */",
	} {
		t.Run(fmt.Sprintf("script_%d", len(sql)), func(t *testing.T) {
			b := sqliteBackend(t)
			files := fstest.MapFS{}
			contracttest.Pair(files, 1, sql, "-- xmigrator:noop")
			s, _ := x.NewSource(files, ".")
			m, _ := x.New(s, b.Driver)
			if _, e := m.Up(t.Context(), 0); !errors.Is(e, x.ErrUnsafeSQL) {
				t.Fatal(e)
			}
			n, e := b.Count(t.Context(), "SELECT count(*) FROM sqlite_schema WHERE name NOT GLOB 'sqlite_*'")
			if e != nil || n != 0 {
				t.Fatalf("unsafe SQL changed schema before rejection: %d %v", n, e)
			}
		})
	}
}

func TestSQLitePrivateMemory(t *testing.T) {
	db, e := sql.Open("sqlite", ":memory:")
	if e != nil {
		t.Fatal(e)
	}
	db.SetMaxOpenConns(1)
	t.Cleanup(func() { db.Close() })
	d, e := sqdriver.FromDB(db, sqdriver.Config{Memory: true})
	if e != nil {
		t.Fatal(e)
	}
	second, e := sqdriver.FromDB(db, sqdriver.Config{Memory: true})
	if e != nil {
		t.Fatal(e)
	}
	e = d.WithSession(t.Context(), func(s x.Session[sqdriver.Tx]) error {
		if e := s.EnsureMetadata(t.Context()); e != nil {
			return e
		}
		ctx, cancel := context.WithTimeout(t.Context(), 20*time.Millisecond)
		defer cancel()
		calls := 0
		e := second.WithSession(ctx, func(x.Session[sqdriver.Tx]) error { calls++; return nil })
		if !errors.Is(e, context.DeadlineExceeded) || calls != 0 {
			t.Fatalf("private-memory run was not serialized: calls=%d error=%v", calls, e)
		}
		return nil
	})
	if e != nil {
		t.Fatal(e)
	}
}

func TestPanicRollsBackSQLAndHistory(t *testing.T) {
	t.Run("SQLite", func(t *testing.T) { b := sqliteBackend(t); panicContract(t, b) })
	t.Run("PostgreSQL", func(t *testing.T) { b := pgBackend(t); panicContract(t, b) })
}

func panicContract[T any](t *testing.T, b contracttest.Backend[T]) {
	t.Helper()
	files := fstest.MapFS{}
	contracttest.Pair(files, 1, "CREATE TABLE entries(id INTEGER);", "DROP TABLE entries;")
	contracttest.Pair(files, 2, "-- xmigrator:hooks=p1\nINSERT INTO entries VALUES(1);", "DELETE FROM entries;")
	source, _ := x.NewSource(files, ".")
	calls := 0
	m, e := x.New(source, b.Driver, x.WithHooks(map[x.Version]x.Hooks[T]{2: {UpRevision: "p1", AfterUp: func(context.Context, T) error { calls++; panic("fixture panic") }}}))
	if e != nil {
		t.Fatal(e)
	}
	func() {
		defer func() {
			if value := recover(); value != "fixture panic" {
				t.Fatalf("panic changed: %v", value)
			}
		}()
		m.Up(t.Context(), 0)
	}()
	n, e := b.Count(t.Context(), "SELECT count(*) FROM entries")
	if e != nil || n != 0 || calls != 1 {
		t.Fatalf("panic rollback: rows=%d callbacks=%d err=%v", n, calls, e)
	}
	status, e := m.Status(t.Context())
	if e != nil || status.Migrations[1].State != "pending" {
		t.Fatalf("panic history %+v %v", status, e)
	}
}

func TestPGConfigFactoryPreservesDialer(t *testing.T) {
	dsn := os.Getenv("XMIGRATOR_TEST_PG_DSN")
	if dsn == "" {
		if testing.Short() {
			t.Skip("PostgreSQL integration")
		}
		t.Fatal("XMIGRATOR_TEST_PG_DSN required")
	}
	cfg, e := pgx.ParseConfig(dsn)
	if e != nil {
		t.Fatal(e)
	}
	original := cfg.DialFunc
	calls := 0
	cfg.DialFunc = func(ctx context.Context, network, address string) (net.Conn, error) {
		calls++
		return original(ctx, network, address)
	}
	meta := fmt.Sprintf("config_meta_%d", time.Now().UnixNano())
	d, e := pgdriver.FromConfig(cfg, pgdriver.Config{MetadataSchema: meta})
	if e != nil {
		t.Fatal(e)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.WithoutCancel(t.Context()), 5*time.Second)
		defer cancel()
		c, e := pgx.Connect(ctx, dsn)
		if e != nil {
			t.Error(e)
			return
		}
		defer c.Close(ctx)
		if _, e = c.Exec(ctx, "DROP SCHEMA IF EXISTS "+meta+" CASCADE"); e != nil {
			t.Error(e)
		}
	})
	files := fstest.MapFS{}
	contracttest.Pair(files, 1, "SELECT 1;", "-- xmigrator:noop")
	s, _ := x.NewSource(files, ".")
	m, _ := x.New(s, d)
	if _, e = m.Up(t.Context(), 0); e != nil {
		t.Fatal(e)
	}
	if _, e = m.Status(t.Context()); e != nil {
		t.Fatal(e)
	}
	if calls != 2 {
		t.Fatalf("custom dialer was lost or used redundantly: %d calls", calls)
	}
}
