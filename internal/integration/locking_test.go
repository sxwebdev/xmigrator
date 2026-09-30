package integration_test

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	x "github.com/sxwebdev/xmigrator"
	sqdriver "github.com/sxwebdev/xmigrator/driver/sqlite"
)

func TestSQLiteLockProcessHelper(t *testing.T) {
	if os.Getenv("XMIGRATOR_LOCK_HELPER") != "1" {
		return
	}
	db, e := sql.Open("sqlite", os.Getenv("XMIGRATOR_LOCK_DB"))
	if e != nil {
		t.Fatal(e)
	}
	t.Cleanup(func() { db.Close() })
	if e = db.PingContext(t.Context()); e != nil {
		t.Fatal(e)
	}
	d, e := sqdriver.FromDB(db, sqdriver.Config{})
	if e != nil {
		t.Fatal(e)
	}
	ready := os.Getenv("XMIGRATOR_LOCK_READY")
	if e = os.WriteFile(ready, []byte("ready"), 0o600); e != nil {
		t.Fatal(e)
	}
	ctx := t.Context()
	if os.Getenv("XMIGRATOR_LOCK_CANCEL") == "1" {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, 80*time.Millisecond)
		defer cancel()
	}
	e = d.WithSession(ctx, func(x.Session[sqdriver.Tx]) error { return os.WriteFile(ready+".acquired", []byte("acquired"), 0o600) })
	if os.Getenv("XMIGRATOR_LOCK_CANCEL") == "1" {
		if !errors.Is(e, context.DeadlineExceeded) {
			t.Fatal(e)
		}
	} else if e != nil {
		t.Fatal(e)
	}
}

func TestSQLiteProcessLockAcrossCommittedTransactions(t *testing.T) {
	if testing.Short() {
		t.Skip("process locking test")
	}
	for _, cancelChild := range []bool{false, true} {
		t.Run(map[bool]string{false: "wait", true: "cancel"}[cancelChild], func(t *testing.T) {
			dir := t.TempDir()
			path := filepath.Join(dir, "app.db")
			ready := filepath.Join(dir, "ready")
			db, e := sql.Open("sqlite", path)
			if e != nil {
				t.Fatal(e)
			}
			t.Cleanup(func() { db.Close() })
			d, e := sqdriver.FromDB(db, sqdriver.Config{})
			if e != nil {
				t.Fatal(e)
			}
			child := exec.CommandContext(t.Context(), os.Args[0], "-test.run=^TestSQLiteLockProcessHelper$", "-test.timeout=10s")
			child.Env = append(os.Environ(), "XMIGRATOR_LOCK_HELPER=1", "XMIGRATOR_LOCK_DB="+path, "XMIGRATOR_LOCK_READY="+ready)
			if cancelChild {
				child.Env = append(child.Env, "XMIGRATOR_LOCK_CANCEL=1")
			}
			var childStarted bool
			t.Cleanup(func() {
				if childStarted && child.ProcessState == nil {
					child.Process.Kill()
					child.Wait()
				}
			})
			e = d.WithSession(t.Context(), func(s x.Session[sqdriver.Tx]) error {
				if e := s.EnsureMetadata(t.Context()); e != nil {
					return e
				}
				out, e := s.InTx(t.Context(), x.Script{Kind: x.DownNoop}, func(tx x.Transaction[sqdriver.Tx]) error {
					_, e := tx.Executor().ExecContext(t.Context(), "CREATE TABLE between_steps(id INTEGER)")
					return e
				})
				if e != nil || out != x.TxCommitted {
					t.Fatalf("first transaction %v %v", out, e)
				}
				if e = child.Start(); e != nil {
					return e
				}
				childStarted = true
				deadline := time.Now().Add(5 * time.Second)
				for {
					if _, e = os.Stat(ready); e == nil {
						break
					}
					if time.Now().After(deadline) {
						t.Fatal("child didn't become ready")
					}
					time.Sleep(5 * time.Millisecond)
				}
				if cancelChild {
					if e = child.Wait(); e != nil {
						t.Fatal(e)
					}
				} else {
					time.Sleep(100 * time.Millisecond)
				}
				if _, e = os.Stat(ready + ".acquired"); !errors.Is(e, os.ErrNotExist) {
					t.Fatal("another process entered the run between committed transactions", e)
				}
				return nil
			})
			if e != nil {
				t.Fatal(e)
			}
			if !cancelChild {
				if e = child.Wait(); e != nil {
					t.Fatal(e)
				}
				if _, e = os.Stat(ready + ".acquired"); e != nil {
					t.Fatal("child didn't acquire after release", e)
				}
			}
		})
	}
}
