// Package contracttest exercises observable migration behavior against every backend.
package contracttest

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"testing/fstest"

	x "github.com/sxwebdev/xmigrator"
)

type Backend[T any] struct {
	Driver  x.Driver[T]
	Count   func(context.Context, string) (int, error)
	Execute func(context.Context, T, string) error
}
type Factory[T any] func(*testing.T) Backend[T]

func Pair(files fstest.MapFS, v int, up, down string) {
	stem := fmt.Sprintf("%d_m%d", v, v)
	files[stem+".up.sql"] = &fstest.MapFile{Data: []byte(up)}
	files[stem+".down.sql"] = &fstest.MapFile{Data: []byte(down)}
}

func Run[T any](t *testing.T, f Factory[T]) {
	t.Helper()
	newMigrator := func(t *testing.T, b Backend[T], files fstest.MapFS, opts ...x.Option[T]) *x.Migrator[T] {
		t.Helper()
		s, e := x.NewSource(files, ".")
		if e != nil {
			t.Fatal(e)
		}
		m, e := x.New(s, b.Driver, opts...)
		if e != nil {
			t.Fatal(e)
		}
		return m
	}
	count := func(t *testing.T, b Backend[T], query string, want int) {
		t.Helper()
		n, e := b.Count(t.Context(), query)
		if e != nil || n != want {
			t.Fatalf("query %q: got %d, error %v, want %d", query, n, e, want)
		}
	}
	t.Run("late_version_reverse_application_and_reapply", func(t *testing.T) {
		b := f(t)
		files := fstest.MapFS{}
		Pair(files, 100, "CREATE TABLE entries(id INTEGER PRIMARY KEY);", "DROP TABLE entries;")
		Pair(files, 300, "CREATE TABLE other(id INTEGER PRIMARY KEY);", "DROP TABLE other;")
		m := newMigrator(t, b, files)
		status, e := m.Status(t.Context())
		if e != nil || len(status.Migrations) != 2 || status.Migrations[0].State != "pending" {
			t.Fatalf("initial status: %+v %v", status, e)
		}
		r, e := m.Up(t.Context(), 0)
		if e != nil || len(r.Actions) != 2 {
			t.Fatalf("up: %+v %v", r, e)
		}
		Pair(files, 200, "INSERT INTO entries VALUES(2);", "DELETE FROM entries WHERE id=2;")
		r, e = m.Up(t.Context(), 1)
		if e != nil || len(r.Actions) != 1 || r.Actions[0].Version != 200 || !r.Actions[0].OutOfOrder {
			t.Fatalf("late up: %+v %v", r, e)
		}
		count(t, b, "SELECT count(*) FROM entries", 1)
		r, e = m.Down(t.Context(), 1)
		if e != nil || len(r.Actions) != 1 || r.Actions[0].Version != 200 {
			t.Fatalf("down: %+v %v", r, e)
		}
		count(t, b, "SELECT count(*) FROM entries", 0)
		r, e = m.Down(t.Context(), 1)
		if e != nil || r.Actions[0].Version != 300 {
			t.Fatalf("second down: %+v %v", r, e)
		}
		r, e = m.Up(t.Context(), 0)
		if e != nil || len(r.Actions) != 2 || r.Actions[0].Version != 200 || r.Actions[1].Version != 300 {
			t.Fatalf("reapply: %+v %v", r, e)
		}
		r, e = m.Up(t.Context(), 0)
		if e != nil || len(r.Actions) != 0 {
			t.Fatalf("idempotent: %+v %v", r, e)
		}
		r, e = m.DownAll(t.Context())
		if e != nil || len(r.Actions) != 3 || r.Actions[0].Version != 300 || r.Actions[1].Version != 200 {
			t.Fatalf("down all: %+v %v", r, e)
		}
		if e = m.Drop(t.Context()); e != nil {
			t.Fatal(e)
		}
	})
	for _, stage := range []string{"before", "sql", "after"} {
		t.Run("rollback_"+stage, func(t *testing.T) {
			b := f(t)
			files := fstest.MapFS{}
			Pair(files, 1, "CREATE TABLE entries(id INTEGER PRIMARY KEY);", "DROP TABLE entries;")
			m := newMigrator(t, b, files)
			if _, e := m.Up(t.Context(), 0); e != nil {
				t.Fatal(e)
			}
			failure := errors.New("hook failed")
			calls := 0
			hook := func(ctx context.Context, tx T) error {
				calls++
				if e := b.Execute(ctx, tx, "INSERT INTO entries VALUES(9);"); e != nil {
					return e
				}
				return failure
			}
			h := x.Hooks[T]{UpRevision: "r1"}
			sql := "-- xmigrator:hooks=r1\nCREATE TABLE rolled_back(id INTEGER);INSERT INTO entries VALUES(3);"
			switch stage {
			case "before":
				h.BeforeUp = hook
			case "after":
				h.AfterUp = hook
			case "sql":
				h.AfterUp = func(context.Context, T) error { calls++; return nil }
				sql += "INSERT INTO nonexistent VALUES(1);"
			}
			Pair(files, 2, sql, "DROP TABLE rolled_back;")
			m = newMigrator(t, b, files, x.WithHooks(map[x.Version]x.Hooks[T]{2: h}))
			r, e := m.Up(t.Context(), 0)
			if e == nil || len(r.Actions) != 0 {
				t.Fatalf("rollback: %+v %v", r, e)
			}
			if stage != "sql" && !errors.Is(e, failure) {
				t.Fatal(e)
			}
			want := 1
			if stage == "sql" {
				want = 0
			}
			if calls != want {
				t.Fatalf("callback ran %d times, want %d", calls, want)
			}
			count(t, b, "SELECT count(*) FROM entries", 0)
			status, e := m.Status(t.Context())
			if e != nil || status.Migrations[1].State != "pending" {
				t.Fatalf("history after rollback %+v %v", status, e)
			}
			Pair(files, 2, "CREATE TABLE rolled_back(id INTEGER);INSERT INTO entries VALUES(3);", "DROP TABLE rolled_back;DELETE FROM entries;")
			m = newMigrator(t, b, files)
			if _, e = m.Up(t.Context(), 0); e != nil {
				t.Fatal(e)
			}
			count(t, b, "SELECT count(*) FROM entries", 1)
		})
	}
	t.Run("down_prevalidates_whole_selection_and_repair", func(t *testing.T) {
		b := f(t)
		files := fstest.MapFS{}
		Pair(files, 1, "CREATE TABLE entries(id INTEGER);", "-- xmigrator:irreversible\n")
		Pair(files, 2, "INSERT INTO entries VALUES(2);", "DELETE FROM entries;")
		m := newMigrator(t, b, files)
		if _, e := m.Up(t.Context(), 0); e != nil {
			t.Fatal(e)
		}
		if r, e := m.DownAll(t.Context()); !errors.Is(e, x.ErrIrreversible) || len(r.Actions) != 0 {
			t.Fatalf("prevalidate: %+v %v", r, e)
		}
		count(t, b, "SELECT count(*) FROM entries", 1)
		Pair(files, 1, "CREATE TABLE entries(id INTEGER);", "DROP TABLE entries;")
		p, e := m.PlanRepairDown(t.Context(), 1)
		if e != nil || !p.WouldChange || p.Before.Kind != x.DownIrreversible || p.After.Kind != x.DownSQL {
			t.Fatalf("repair plan %+v %v", p, e)
		}
		if _, e = m.DownAll(t.Context()); !errors.Is(e, x.ErrDefinitionMismatch) {
			t.Fatal(e)
		}
		r, e := m.RepairDown(t.Context(), 1)
		if e != nil || !r.Confirmed || !r.Changed {
			t.Fatalf("repair: %+v %v", r, e)
		}
		r, e = m.RepairDown(t.Context(), 1)
		if e != nil || !r.Confirmed || r.Changed {
			t.Fatalf("noop repair: %+v %v", r, e)
		}
		if _, e = m.DownAll(t.Context()); e != nil {
			t.Fatal(e)
		}
	})
	for _, policy := range []x.ChecksumPolicy{x.ChecksumStrict, x.ChecksumWarn, x.ChecksumDisabled} {
		t.Run(fmt.Sprintf("checksum_policy_%d", policy), func(t *testing.T) {
			b := f(t)
			files := fstest.MapFS{}
			Pair(files, 1, "CREATE TABLE entries(id INTEGER);", "DROP TABLE entries;")
			m := newMigrator(t, b, files, x.WithChecksumPolicy[T](policy))
			if _, e := m.Up(t.Context(), 0); e != nil {
				t.Fatal(e)
			}
			Pair(files, 1, "CREATE TABLE entries(id INTEGER); -- changed\n", "DROP TABLE entries;")
			r, e := m.Up(t.Context(), 0)
			if policy == x.ChecksumStrict {
				if !errors.Is(e, x.ErrChecksumMismatch) {
					t.Fatal(e)
				}
			} else if e != nil {
				t.Fatal(e)
			}
			if len(r.Actions) != 0 {
				t.Fatal("already applied SQL executed")
			}
			repair, e := m.RepairDown(t.Context(), 1)
			if e != nil || !repair.Confirmed {
				t.Fatalf("repair accepts only down: %+v %v", repair, e)
			}
			_, e = m.DownAll(t.Context())
			if policy == x.ChecksumStrict {
				if !errors.Is(e, x.ErrChecksumMismatch) {
					t.Fatal(e)
				}
			} else if e != nil {
				t.Fatal(e)
			}
		})
	}
	t.Run("unknown_applied_policy", func(t *testing.T) {
		b := f(t)
		files := fstest.MapFS{}
		Pair(files, 1, "CREATE TABLE entries(id INTEGER);", "DROP TABLE entries;")
		m := newMigrator(t, b, files)
		if _, e := m.Up(t.Context(), 0); e != nil {
			t.Fatal(e)
		}
		delete(files, "1_m1.up.sql")
		delete(files, "1_m1.down.sql")
		status, e := m.Status(t.Context())
		if e != nil || len(status.Migrations) != 1 || status.Migrations[0].State != "unknown_applied" {
			t.Fatalf("unknown status %+v %v", status, e)
		}
		r, e := m.Up(t.Context(), 0)
		if e != nil || len(r.Issues) != 1 {
			t.Fatalf("allow %+v %v", r, e)
		}
		strict := newMigrator(t, b, files, x.WithUnknownAppliedPolicy[T](x.UnknownAppliedError))
		if _, e = strict.Up(t.Context(), 0); !errors.Is(e, x.ErrUnknownApplied) {
			t.Fatal(e)
		}
		if _, e = m.DownAll(t.Context()); !errors.Is(e, x.ErrUnknownApplied) {
			t.Fatal(e)
		}
		count(t, b, "SELECT count(*) FROM entries", 0)
	})
	t.Run("unsafe_script_is_rejected_before_effects", func(t *testing.T) {
		b := f(t)
		files := fstest.MapFS{}
		Pair(files, 1, "CREATE TABLE entries(id INTEGER);COMMIT;BEGIN;", "DROP TABLE entries;")
		m := newMigrator(t, b, files)
		if _, e := m.Up(t.Context(), 0); !errors.Is(e, x.ErrUnsafeSQL) {
			t.Fatal(e)
		}
	})
}
