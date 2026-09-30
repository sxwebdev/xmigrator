package integration_test

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"testing/fstest"
	"time"

	"github.com/jackc/pgx/v5"
	x "github.com/sxwebdev/xmigrator"
	"github.com/sxwebdev/xmigrator/internal/contracttest"
)

func TestPGDropRejectsExternalDependency(t *testing.T) {
	b := pgBackend(t)
	files := fstest.MapFS{}
	contracttest.Pair(files, 1, "CREATE TABLE entries(id INTEGER PRIMARY KEY);INSERT INTO entries VALUES(1);", "DROP TABLE entries;")
	source, _ := x.NewSource(files, ".")
	m, _ := x.New(source, b.Driver)
	if _, e := m.Up(t.Context(), 0); e != nil {
		t.Fatal(e)
	}
	outside := fmt.Sprintf("external_%d", time.Now().UnixNano())
	ddl := func(ctx context.Context, query string) error {
		return b.Driver.WithSession(ctx, func(s x.Session[pgx.Tx]) error {
			_, e := s.InTx(ctx, x.Script{}, func(tx x.Transaction[pgx.Tx]) error { return b.Execute(ctx, tx.Executor(), query) })
			return e
		})
	}
	if e := ddl(t.Context(), "CREATE SCHEMA "+outside+";CREATE TABLE "+outside+".child(id INTEGER REFERENCES entries(id));INSERT INTO "+outside+".child VALUES(1);"); e != nil {
		t.Fatal(e)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.WithoutCancel(t.Context()), 5*time.Second)
		defer cancel()
		if e := ddl(ctx, "DROP SCHEMA IF EXISTS "+outside+" CASCADE"); e != nil {
			t.Error(e)
		}
	})
	if e := m.Drop(t.Context()); !errors.Is(e, x.ErrDropScope) {
		t.Fatal(e)
	}
	for _, query := range []string{"SELECT count(*) FROM entries", "SELECT count(*) FROM " + outside + ".child"} {
		n, e := b.Count(t.Context(), query)
		if e != nil || n != 1 {
			t.Fatalf("drop escaped/partially changed scope: %s count=%d error=%v", query, n, e)
		}
	}
	status, e := m.Status(t.Context())
	if e != nil || status.Migrations[0].State != "applied" {
		t.Fatalf("drop lost history %+v %v", status, e)
	}
}

func TestPGDropObjectKindsAndSchemaPreservation(t *testing.T) {
	b := pgBackend(t)
	files := fstest.MapFS{}
	contracttest.Pair(files, 1, `CREATE TYPE category AS ENUM ('a','b');CREATE DOMAIN positive AS integer CHECK(VALUE>0);CREATE AGGREGATE sum_ids(integer) (SFUNC=int4pl,STYPE=integer,INITCOND=0);CREATE TABLE parent(id SERIAL PRIMARY KEY,c category);CREATE TABLE child(id INTEGER REFERENCES parent(id));CREATE VIEW first_view AS SELECT * FROM parent;CREATE VIEW second_view AS SELECT * FROM first_view;CREATE MATERIALIZED VIEW mv AS SELECT * FROM second_view;CREATE FUNCTION answer() RETURNS category LANGUAGE SQL BEGIN ATOMIC SELECT 'a'::category; END;CREATE PROCEDURE p() LANGUAGE SQL BEGIN ATOMIC SELECT 1; END;`, "-- xmigrator:irreversible")
	s, _ := x.NewSource(files, ".")
	m, _ := x.New(s, b.Driver)
	if _, e := m.Up(t.Context(), 0); e != nil {
		t.Fatal(e)
	}
	if e := m.Drop(t.Context()); e != nil {
		t.Fatal(e)
	}
	n, e := b.Count(t.Context(), "SELECT count(*) FROM pg_catalog.pg_class WHERE relnamespace=current_schema()::regnamespace AND relkind IN ('r','v','m','S')")
	if e != nil || n != 0 {
		t.Fatalf("objects remain: %d %v", n, e)
	}
	if _, e = m.Up(t.Context(), 0); e != nil {
		t.Fatal("schema/grants weren't preserved", e)
	}
}

func TestSQLiteDropVirtualTablesAndSimilarSystemName(t *testing.T) {
	b := sqliteBackend(t)
	files := fstest.MapFS{}
	contracttest.Pair(files, 1, "CREATE VIRTUAL TABLE docs USING fts5(body);INSERT INTO docs VALUES('hello');CREATE TABLE sqliteXtable(id INTEGER);CREATE TABLE entries(id INTEGER);CREATE VIEW v AS SELECT * FROM entries;CREATE TRIGGER tr AFTER INSERT ON entries BEGIN INSERT INTO sqliteXtable VALUES(NEW.id);END;", "-- xmigrator:irreversible")
	s, _ := x.NewSource(files, ".")
	m, _ := x.New(s, b.Driver)
	if _, e := m.Up(t.Context(), 0); e != nil {
		t.Fatal(e)
	}
	if e := m.Drop(t.Context()); e != nil {
		t.Fatal(e)
	}
	n, e := b.Count(t.Context(), "SELECT count(*) FROM sqlite_schema WHERE name='sqliteXtable' OR name GLOB 'docs*' OR name IN ('entries','v','tr')")
	if e != nil || n != 0 {
		t.Fatalf("objects survived drop: count=%d error=%v", n, e)
	}
}

func TestPGDropRefusesUnsupportedObjectClass(t *testing.T) {
	b := pgBackend(t)
	files := fstest.MapFS{}
	contracttest.Pair(files, 1, `CREATE TABLE entries(id INTEGER);INSERT INTO entries VALUES(1);CREATE COLLATION own_collation FROM "C";`, "-- xmigrator:irreversible")
	s, _ := x.NewSource(files, ".")
	m, _ := x.New(s, b.Driver)
	if _, e := m.Up(t.Context(), 0); e != nil {
		t.Fatal(e)
	}
	if e := m.Drop(t.Context()); !errors.Is(e, x.ErrDropScope) {
		t.Fatal(e)
	}
	n, e := b.Count(t.Context(), "SELECT count(*) FROM entries")
	if e != nil || n != 1 {
		t.Fatalf("unsupported drop partially changed scope: %d %v", n, e)
	}
}
