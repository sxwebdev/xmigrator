package sqlite

import (
	"context"
	"errors"
	"os"
	"testing"
	"time"

	x "github.com/sxwebdev/xmigrator"
)

func TestConfigAndBorrowedConnection(t *testing.T) {
	if _, e := FromDB(nil, Config{}); !errors.Is(e, x.ErrInvalidConfig) {
		t.Fatal(e)
	}
	d, f := engine(t)
	for _, cfg := range []Config{{MetadataPrefix: "BAD"}, {BusyTimeout: -1}, {LockWaitTimeout: -1}, {BusyTimeout: time.Nanosecond}} {
		if _, e := FromDB(d.db, cfg); !errors.Is(e, x.ErrInvalidConfig) {
			t.Fatal(e)
		}
	}
	d.cfg.LockWaitTimeout = time.Second
	calls := 0
	e := d.WithSession(t.Context(), func(s x.Session[Tx]) error {
		calls++
		_, e := s.InTx(t.Context(), x.Script{Kind: x.DownNoop}, func(tx x.Transaction[Tx]) error {
			executor := tx.Executor()
			if _, e := executor.ExecContext(t.Context(), "SELECT 1;"); e != nil {
				return e
			}
			rows, e := executor.QueryContext(t.Context(), "SELECT 1")
			if e != nil {
				return e
			}
			rows.Close()
			var n int
			if e := executor.QueryRowContext(t.Context(), "SELECT 1").Scan(&n); e != nil {
				return e
			}
			_, e = executor.PrepareContext(t.Context(), "SELECT 1")
			if !errors.Is(e, fault) {
				t.Fatal(e)
			}
			return nil
		})
		return e
	})
	if e != nil || calls != 1 || f.fk != 1 || f.busy != 17 {
		t.Fatalf("borrowed state: fk=%d busy=%d calls=%d error=%v", f.fk, f.busy, calls, e)
	}
	if e = d.db.PingContext(t.Context()); e != nil {
		t.Fatal("caller DB was closed", e)
	}
}

func TestConnectionFailures(t *testing.T) {
	for _, tt := range []struct {
		name, query, exec string
		off               bool
	}{
		{"database_list", "database_list", "", false}, {"foreign_keys", "foreign_keys", "", false}, {"busy_timeout", "busy_timeout", "", false}, {"journal_mode", "journal_mode", "", false}, {"busy_setup", "", "busy_timeout=17", false}, {"journal_off", "", "", true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			d, f := engine(t)
			f.failQuery = tt.query
			f.failExec = tt.exec
			if tt.off {
				f.journal = "OFF"
			}
			calls := 0
			e := d.WithSession(t.Context(), func(x.Session[Tx]) error { calls++; return nil })
			want := fault
			if tt.off {
				want = x.ErrUnsafeSQL
			}
			if !errors.Is(e, want) || calls != 0 {
				t.Fatalf("effects=%d err=%v", calls, e)
			}
		})
	}
	d, f := engine(t)
	f.onCommit = func() { f.resetFail = true }
	e := d.WithSession(t.Context(), func(s x.Session[Tx]) error {
		out, e := s.InTx(t.Context(), x.Script{Kind: x.DownNoop}, func(x.Transaction[Tx]) error { return nil })
		if out != x.TxCommitted || !errors.Is(e, fault) {
			t.Fatalf("commit outcome=%v err=%v", out, e)
		}
		return e
	})
	if !errors.Is(e, fault) {
		t.Fatal(e)
	}
	d, _ = engine(t)
	d.db.Close()
	if e = d.WithSession(t.Context(), func(x.Session[Tx]) error { return nil }); e == nil {
		t.Fatal("closed DB accepted")
	}
	if _, e = d.ReadHistorySnapshot(t.Context()); e == nil {
		t.Fatal("closed snapshot accepted")
	}
}

func TestTransactionFailures(t *testing.T) {
	for _, tt := range []struct {
		name, query, exec string
		callbackErr       bool
		fkBad             bool
		out               x.TxOutcome
		want              error
	}{
		{"FK_setup", "", "foreign_keys=1", false, false, x.TxNotCommitted, fault},
		{"begin", "", "BEGIN IMMEDIATE", false, false, x.TxNotCommitted, fault},
		{"callback", "", "", true, false, x.TxNotCommitted, fault},
		{"FK_query", "foreign_key_check", "", false, false, x.TxNotCommitted, fault},
		{"FK_violation", "", "", false, true, x.TxNotCommitted, x.ErrForeignKey},
		{"commit", "", "COMMIT", false, false, x.TxNotCommitted, fault},
		{"unknown_commit", "", "COMMIT", false, false, x.TxUnknown, x.ErrCommitOutcomeUnknown},
	} {
		t.Run(tt.name, func(t *testing.T) {
			d, f := engine(t)
			f.failQuery = tt.query
			f.failExec = tt.exec
			f.fkBad = tt.fkBad
			f.failRollback = tt.name == "unknown_commit"
			s := sessionFor(t, d)
			off := false
			script := x.Script{Kind: x.DownNoop}
			if tt.fkBad || tt.name == "FK_query" {
				script.ForeignKeys = &off
			}
			out, e := s.InTx(t.Context(), script, func(x.Transaction[Tx]) error {
				if tt.callbackErr {
					return fault
				}
				return nil
			})
			if out != tt.out || !errors.Is(e, tt.want) {
				t.Fatalf("outcome=%v err=%v", out, e)
			}
		})
	}
	d, f := engine(t)
	s := sessionFor(t, d)
	f.failExec = "ROLLBACK"
	if _, e := s.InTx(t.Context(), x.Script{Kind: x.DownNoop}, func(x.Transaction[Tx]) error { return fault }); !errors.Is(e, fault) {
		t.Fatal(e)
	}
	d, f = engine(t)
	s = sessionFor(t, d)
	f.rowsErr = true
	off := false
	if _, e := s.InTx(t.Context(), x.Script{Kind: x.DownNoop, ForeignKeys: &off}, func(x.Transaction[Tx]) error { return nil }); !errors.Is(e, fault) {
		t.Fatal(e)
	}
}

func TestTransactionMetadataWrites(t *testing.T) {
	d, f := engine(t)
	s := sessionFor(t, d)
	tx := &transaction{s}
	r := x.Record{Version: 1, Name: "users", DownKind: x.DownSQL, AppliedAt: time.Now().UTC()}
	if e := tx.Insert(t.Context(), r); e != nil {
		t.Fatal(e)
	}
	if e := tx.Delete(t.Context(), 1); e != nil {
		t.Fatal(e)
	}
	if e := tx.UpdateDownMetadata(t.Context(), 1, x.DownDefinition{Kind: x.DownSQL}); e != nil {
		t.Fatal(e)
	}
	if e := tx.ExecScript(t.Context(), x.Script{SQL: "COMMIT;"}); !errors.Is(e, x.ErrUnsafeSQL) {
		t.Fatal(e)
	}
	if e := tx.ExecScript(t.Context(), x.Script{SQL: "SELECT 1;"}); e != nil {
		t.Fatal(e)
	}
	f.affected = 0
	if e := tx.Delete(t.Context(), 1); !errors.Is(e, x.ErrHistoryConflict) {
		t.Fatal(e)
	}
	f.affectedErr = true
	if e := tx.Delete(t.Context(), 1); !errors.Is(e, fault) {
		t.Fatal(e)
	}
	f.failExec = "DELETE"
	if e := tx.Delete(t.Context(), 1); !errors.Is(e, fault) {
		t.Fatal(e)
	}
	f.failQuery = "SELECT version"
	if _, e := tx.ReadHistory(t.Context()); !errors.Is(e, fault) {
		t.Fatal(e)
	}
}

func TestFileAndMemoryLocks(t *testing.T) {
	d, f := engine(t)
	c, e := d.db.Conn(t.Context())
	if e != nil {
		t.Fatal(e)
	}
	t.Cleanup(func() { c.Close() })
	release, e := d.lock(t.Context(), c)
	if e != nil {
		t.Fatal(e)
	}
	db2, e := FromDB(d.db, Config{})
	if e != nil {
		t.Fatal(e)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 20*time.Millisecond)
	defer cancel()
	if _, e := db2.lock(ctx, c); !errors.Is(e, context.DeadlineExceeded) {
		t.Fatal(e)
	}
	if e = release(); e != nil {
		t.Fatal(e)
	}
	if _, e = os.Stat(f.path + ".xmigrator.lock"); e != nil {
		t.Fatal("persistent lock deleted", e)
	}
	canceled, stop := context.WithCancel(t.Context())
	stop()
	if _, e = d.lock(canceled, c); !errors.Is(e, context.Canceled) {
		t.Fatal(e)
	}
	f.path = ""
	if _, e = d.lock(t.Context(), c); !errors.Is(e, x.ErrInvalidConfig) {
		t.Fatal(e)
	}
	d.cfg.Memory = true
	release, e = d.lock(t.Context(), c)
	if e != nil {
		t.Fatal(e)
	}
	if e = release(); e != nil {
		t.Fatal(e)
	}
	d.cfg.LockIdentity = os.TempDir() + "/xmigrator-explicit-test"
	release, e = d.lock(t.Context(), c)
	if e != nil {
		t.Fatal(e)
	}
	release()
	t.Cleanup(func() { os.Remove(d.cfg.LockIdentity + ".xmigrator.lock") })
	f.path = "/does/not/exist"
	d.cfg.LockIdentity = ""
	if _, e = d.lock(t.Context(), c); !errors.Is(e, os.ErrNotExist) {
		t.Fatal(e)
	}
}

func TestDatabaseListAndRestoreFailures(t *testing.T) {
	d, f := engine(t)
	f.badQuery = "database_list"
	if e := d.WithSession(t.Context(), func(x.Session[Tx]) error { return nil }); e == nil {
		t.Fatal("invalid path rows accepted")
	}
	d, f = engine(t)
	f.rowsErr = true
	if e := d.WithSession(t.Context(), func(x.Session[Tx]) error { return nil }); !errors.Is(e, fault) {
		t.Fatal(e)
	}
	d, f = engine(t)
	f.failExec = "busy_timeout=17"
	if e := d.WithSession(t.Context(), func(x.Session[Tx]) error { return nil }); !errors.Is(e, fault) {
		t.Fatal(e)
	}
	d, f = engine(t)
	if e := os.Mkdir(f.path+".xmigrator.lock", 0o700); e != nil {
		t.Fatal(e)
	}
	if e := d.WithSession(t.Context(), func(x.Session[Tx]) error { return nil }); e == nil {
		t.Fatal("directory accepted as lockfile")
	}
}

func TestFileLockCancellationBoundaries(t *testing.T) {
	for _, stage := range []string{"closed", "cancel_after_acquire", "cancel_while_waiting"} {
		t.Run(stage, func(t *testing.T) {
			f, e := os.CreateTemp(t.TempDir(), "lock")
			if e != nil {
				t.Fatal(e)
			}
			ctx, cancel := context.WithCancel(t.Context())
			t.Cleanup(cancel)
			try := tryLock
			if stage == "closed" {
				f.Close()
			} else {
				try = func(f *os.File) (bool, error) {
					if stage == "cancel_while_waiting" {
						cancel()
						return false, nil
					}
					ok, e := tryLock(f)
					cancel()
					return ok, e
				}
			}
			release, e := lockFile(ctx, f, try, unlock)
			if release != nil || e == nil {
				t.Fatalf("released=%v error=%v", release != nil, e)
			}
			if stage != "closed" && !errors.Is(e, context.Canceled) {
				t.Fatal(e)
			}
		})
	}
	d, f := engine(t)
	f.path = ""
	d.cfg.LockIdentity = "relative_identity"
	s := sessionFor(t, d)
	if _, e := d.lock(t.Context(), s.c); !errors.Is(e, x.ErrInvalidConfig) {
		t.Fatal(e)
	}
}

func TestPrivateMemoryCancellation(t *testing.T) {
	d, f := engine(t)
	f.path = ""
	d.cfg.Memory = true
	s := sessionFor(t, d)
	ctx, cancel := context.WithCancel(t.Context())
	t.Cleanup(cancel)
	cancel()
	if _, e := d.lock(ctx, s.c); !errors.Is(e, context.Canceled) {
		t.Fatal(e)
	}
}

func TestFileLockAlreadyCanceled(t *testing.T) {
	f, e := os.CreateTemp(t.TempDir(), "lock")
	if e != nil {
		t.Fatal(e)
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if release, e := lockFile(ctx, f, tryLock, unlock); release != nil || !errors.Is(e, context.Canceled) {
		t.Fatalf("release=%t error=%v", release != nil, e)
	}
}
