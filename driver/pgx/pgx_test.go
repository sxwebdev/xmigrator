package pgx

import (
	"context"
	"errors"
	"testing"
	"testing/synctest"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
	x "github.com/sxwebdev/xmigrator"
)

func TestConstructors(t *testing.T) {
	for _, cfg := range []Config{{MetadataSchema: "pg_catalog"}, {MetadataSchema: "BAD"}, {MetadataPrefix: "BAD"}, {LockWaitTimeout: -1}, {DDLLockTimeout: -1}, {StatementTimeout: -1}, {StatementTimeout: time.Nanosecond}, {DDLLockTimeout: 1500 * time.Microsecond}, {DropSchemas: []string{"xmigrator"}}, {DropSchemas: []string{"pg_catalog"}}, {DropSchemas: []string{"app", "app"}}} {
		if _, e := FromDSN("postgres://localhost/test", cfg); e == nil {
			t.Fatal("invalid config accepted", cfg)
		}
	}
	if _, e := FromPool(nil, Config{}); !errors.Is(e, x.ErrInvalidConfig) {
		t.Fatal(e)
	}
	if _, e := FromConfig(nil, Config{}); !errors.Is(e, x.ErrInvalidConfig) {
		t.Fatal(e)
	}
	if _, e := FromDSN("://bad", Config{}); e == nil {
		t.Fatal("bad DSN accepted")
	}
	cfg, e := pgx.ParseConfig("postgres://localhost/test")
	if e != nil {
		t.Fatal(e)
	}
	d, e := FromConfig(cfg, Config{})
	if e != nil {
		t.Fatal(e)
	}
	if d.cfg.MetadataSchema != "xmigrator" {
		t.Fatal(d.cfg)
	}
	if e = d.ValidateScript(x.Script{SQL: "COMMIT"}); !errors.Is(e, x.ErrUnsafeSQL) {
		t.Fatal(e)
	}
	poolCfg, e := pgxpool.ParseConfig("postgres://localhost/test")
	if e != nil {
		t.Fatal(e)
	}
	pool, e := pgxpool.NewWithConfig(t.Context(), poolCfg)
	if e != nil {
		t.Fatal(e)
	}
	t.Cleanup(pool.Close)
	p, e := FromPool(pool, Config{})
	if e != nil {
		t.Fatal(e)
	}
	pool.Close()
	if e = p.WithSession(t.Context(), func(x.Session[pgx.Tx]) error { return nil }); e == nil {
		t.Fatal("closed pool accepted")
	}
}

func TestSessionBoundaries(t *testing.T) {
	d, c := connectionFor(t)
	called := 0
	c.lockBusy = 1
	d.cfg.LockWaitTimeout = time.Second
	if e := d.WithSession(t.Context(), func(x.Session[pgx.Tx]) error { called++; return nil }); e != nil || called != 1 {
		t.Fatalf("calls=%d err=%v", called, e)
	}
	for _, stage := range []string{"acquire", "lock", "unlock", "callback", "close", "unheld"} {
		t.Run(stage, func(t *testing.T) {
			d, c := connectionFor(t)
			switch stage {
			case "acquire":
				d.acquire = func(context.Context) (connection, error) { return nil, fault }
			case "lock":
				c.queryFail = "pg_try_advisory_lock"
			case "unlock":
				c.queryFail = "pg_advisory_unlock"
			case "close":
				c.closeErr = fault
			case "unheld":
				c.unlock = false
			}
			e := d.WithSession(t.Context(), func(x.Session[pgx.Tx]) error {
				if stage == "callback" {
					return fault
				}
				return nil
			})
			want := fault
			if stage == "unheld" {
				want = x.ErrHistoryConflict
			}
			if !errors.Is(e, want) {
				t.Fatal(e)
			}
		})
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if e := d.WithSession(ctx, func(x.Session[pgx.Tx]) error { t.Fatal("canceled lock executed"); return nil }); !errors.Is(e, context.Canceled) {
		t.Fatal(e)
	}
	c.lockBusy = 100
	d.cfg.LockWaitTimeout = time.Millisecond
	if e := d.WithSession(t.Context(), func(x.Session[pgx.Tx]) error { return nil }); !errors.Is(e, context.DeadlineExceeded) {
		t.Fatal(e)
	}
}

func TestTransactionOutcomes(t *testing.T) {
	localized := &pgconn.PgError{Severity: "\u041e\u0428\u0418\u0411\u041a\u0410", SeverityUnlocalized: "ERROR", Code: "23503"}
	rejection := &pgconn.PgError{Severity: "ERROR", SeverityUnlocalized: "ERROR", Code: "23503"}
	for _, tt := range []struct {
		name        string
		setup       func(*fakeConnection)
		callbackErr bool
		out         x.TxOutcome
		want        error
	}{
		{"success", func(*fakeConnection) {}, false, x.TxCommitted, nil},
		{"begin", func(c *fakeConnection) { c.beginErr = fault }, false, x.TxNotCommitted, fault},
		{"settings", func(c *fakeConnection) { c.execFail = "set_config" }, false, x.TxNotCommitted, fault},
		{"callback", func(*fakeConnection) {}, true, x.TxNotCommitted, fault},
		{"state", func(c *fakeConnection) {}, false, x.TxNotCommitted, x.ErrUnsafeSQL},
		{"unknown_commit", func(c *fakeConnection) { c.commitErr = fault }, false, x.TxUnknown, x.ErrCommitOutcomeUnknown},
		{"localized_server_rejection", func(c *fakeConnection) { c.commitErr = localized }, false, x.TxNotCommitted, localized},
		{"server_rejection", func(c *fakeConnection) { c.commitErr = rejection }, false, x.TxNotCommitted, rejection},
		{"fatal_disconnect", func(c *fakeConnection) {
			c.commitErr = &pgconn.PgError{Severity: "FATAL", SeverityUnlocalized: "FATAL", Code: "57P01"}
		}, false, x.TxUnknown, x.ErrCommitOutcomeUnknown},
		{"commit_rollback", func(c *fakeConnection) { c.commitErr = pgx.ErrTxCommitRollback }, false, x.TxNotCommitted, pgx.ErrTxCommitRollback},
		{"rollback", func(c *fakeConnection) { c.rollbackErr = fault }, true, x.TxNotCommitted, fault},
	} {
		t.Run(tt.name, func(t *testing.T) {
			d, c := connectionFor(t)
			tt.setup(c)
			s := &session{d, c}
			out, e := s.InTx(t.Context(), x.Script{}, func(x.Transaction[pgx.Tx]) error {
				if tt.name == "state" {
					c.state = 'I'
				}
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
}

func TestTransactionWrites(t *testing.T) {
	d, c := connectionFor(t)
	c.exists = true
	s := &session{d, c}
	tx := &transaction{s, &fakeTransaction{c: c}}
	_ = tx.Executor()
	if e := tx.ExecScript(t.Context(), x.Script{SQL: "SELECT 1;"}); e != nil {
		t.Fatal(e)
	}
	if e := tx.ExecScript(t.Context(), x.Script{SQL: "COMMIT;"}); !errors.Is(e, x.ErrUnsafeSQL) {
		t.Fatal(e)
	}
	if e := tx.Insert(t.Context(), x.Record{Version: 1, Name: "users"}); e != nil {
		t.Fatal(e)
	}
	if e := tx.Delete(t.Context(), 1); e != nil {
		t.Fatal(e)
	}
	if e := tx.UpdateDownMetadata(t.Context(), 1, x.DownDefinition{}); e != nil {
		t.Fatal(e)
	}
	c.affected = 0
	if e := tx.Delete(t.Context(), 1); !errors.Is(e, x.ErrHistoryConflict) {
		t.Fatal(e)
	}
	if e := tx.UpdateDownMetadata(t.Context(), 1, x.DownDefinition{}); !errors.Is(e, x.ErrHistoryConflict) {
		t.Fatal(e)
	}
	c.history = recordValues()
	if r, e := tx.ReadHistory(t.Context()); e != nil || len(r) != 1 {
		t.Fatalf("%+v %v", r, e)
	}
	if r, e := s.ReadHistory(t.Context()); e != nil || len(r) != 1 {
		t.Fatalf("%+v %v", r, e)
	}
	c.execFail = "UPDATE"
	if e := tx.UpdateDownMetadata(t.Context(), 1, x.DownDefinition{}); !errors.Is(e, fault) {
		t.Fatal(e)
	}
	c.execFail = "DELETE"
	if e := tx.Delete(t.Context(), 1); !errors.Is(e, fault) {
		t.Fatal(e)
	}
}

func TestCancellationAfterLock(t *testing.T) {
	d, c := connectionFor(t)
	ctx, cancel := context.WithCancel(t.Context())
	t.Cleanup(cancel)
	c.onLock = cancel
	calls := 0
	if e := d.WithSession(ctx, func(x.Session[pgx.Tx]) error { calls++; return nil }); !errors.Is(e, context.Canceled) || calls != 0 {
		t.Fatalf("canceled after lock: calls=%d error=%v", calls, e)
	}
}

func TestCanceledNativeConnection(t *testing.T) {
	d, e := FromDSN("postgres://localhost/test", Config{})
	if e != nil {
		t.Fatal(e)
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, e = d.ReadHistorySnapshot(ctx); !errors.Is(e, context.Canceled) {
		t.Fatal(e)
	}
}

func TestWaitingLockWithVirtualTime(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		d, c := connectionFor(t)
		c.lockBusy = 10000
		d.cfg.LockWaitTimeout = time.Second
		done := make(chan error, 1)
		calls := 0
		go func() { done <- d.WithSession(t.Context(), func(x.Session[pgx.Tx]) error { calls++; return nil }) }()
		synctest.Sleep(time.Second)
		if e := <-done; !errors.Is(e, context.DeadlineExceeded) || calls != 0 {
			t.Fatalf("calls=%d error=%v", calls, e)
		}
	})
}

func TestAdvisoryLockBackoffLimitsQueries(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		d, c := connectionFor(t)
		c.lockBusy = 1000
		attempts := 0
		c.onLock = func() { attempts++ }
		ctx, cancel := context.WithTimeout(t.Context(), 2*time.Second)
		defer cancel()
		err := d.WithSession(ctx, func(x.Session[pgx.Tx]) error { t.Fatal("busy lock entered callback"); return nil })
		if !errors.Is(err, context.DeadlineExceeded) || attempts < 5 || attempts > 10 {
			t.Fatalf("unbounded polling: attempts=%d error=%v", attempts, err)
		}
	})
}
