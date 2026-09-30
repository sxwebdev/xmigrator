// Package sqlite implements xmigrator using an application's database/sql SQLite engine.
// It deliberately does not import or register an engine.
package sqlite

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	x "github.com/sxwebdev/xmigrator"
	"github.com/sxwebdev/xmigrator/internal/metadata"
)

type Config struct {
	MetadataPrefix               string
	BusyTimeout, LockWaitTimeout time.Duration
	Memory                       bool
	LockIdentity                 string
}
type Driver struct {
	db     *sql.DB
	cfg    Config
	prefix string
}

// Tx is a pinned connection executor. Only the driver controls commit and rollback.
type Tx interface {
	ExecContext(context.Context, string, ...any) (sql.Result, error)
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
	QueryRowContext(context.Context, string, ...any) *sql.Row
	PrepareContext(context.Context, string) (*sql.Stmt, error)
}
type executor struct{ conn *sql.Conn }

func (t executor) ExecContext(c context.Context, q string, a ...any) (sql.Result, error) {
	return t.conn.ExecContext(c, q, a...)
}

func (t executor) QueryContext(c context.Context, q string, a ...any) (*sql.Rows, error) {
	return t.conn.QueryContext(c, q, a...)
}

func (t executor) QueryRowContext(c context.Context, q string, a ...any) *sql.Row {
	return t.conn.QueryRowContext(c, q, a...)
}

func (t executor) PrepareContext(c context.Context, q string) (*sql.Stmt, error) {
	return t.conn.PrepareContext(c, q)
}

func FromDB(db *sql.DB, cfg Config) (*Driver, error) {
	if db == nil || cfg.BusyTimeout < 0 || cfg.LockWaitTimeout < 0 || cfg.BusyTimeout%time.Millisecond != 0 {
		return nil, x.ErrInvalidConfig
	}
	p, e := metadata.Prefix(cfg.MetadataPrefix)
	if e != nil {
		return nil, e
	}
	return &Driver{db, cfg, p}, nil
}
func (d *Driver) ValidateScript(s x.Script) error { return x.ValidateSQL(s, "sqlite") }
func quote(s string) string                       { return `"` + strings.ReplaceAll(s, `"`, `""`) + `"` }
func (d *Driver) table(suffix string) string      { return "main." + quote(d.prefix+suffix) }
func cleanupContext(ctx context.Context) (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
}

func (d *Driver) lock(ctx context.Context, c *sql.Conn) (func() error, error) {
	if e := ctx.Err(); e != nil {
		return nil, e
	}
	rows, e := c.QueryContext(ctx, "PRAGMA database_list")
	if e != nil {
		return nil, e
	}
	var file string
	for rows.Next() {
		var n int
		var name, p string
		if e = rows.Scan(&n, &name, &p); e != nil {
			break
		}
		if name == "main" {
			file = p
		}
	}
	e = errors.Join(e, rows.Err(), rows.Close())
	if e != nil {
		return nil, e
	}
	if file == "" && d.cfg.LockIdentity == "" {
		if !d.cfg.Memory || d.db.Stats().MaxOpenConnections != 1 {
			return nil, fmt.Errorf("%w: memory database requires Memory and max open connections=1 or LockIdentity", x.ErrInvalidConfig)
		}
		// A single pinned database/sql connection serializes the entire private-memory run.
		return func() error { return nil }, nil
	}
	if d.cfg.LockIdentity != "" && file == "" {
		file = d.cfg.LockIdentity
	} else {
		file, e = filepath.EvalSymlinks(file)
		if e != nil {
			return nil, e
		}
	}
	if !filepath.IsAbs(file) {
		return nil, fmt.Errorf("%w: lock identity must be absolute", x.ErrInvalidConfig)
	}
	f, e := os.OpenFile(file+".xmigrator.lock", os.O_CREATE|os.O_RDWR, 0o600)
	if e != nil {
		return nil, e
	}
	return lockFile(ctx, f, tryLock, unlock)
}

func lockFile(ctx context.Context, f *os.File, try func(*os.File) (bool, error), release func(*os.File) error) (func() error, error) {
	var e error
	for {
		if e = ctx.Err(); e != nil {
			_ = f.Close()
			return nil, e
		}
		ok, err := try(f)
		if err != nil {
			_ = f.Close()
			return nil, err
		}
		if ok {
			if e = ctx.Err(); e != nil {
				_ = release(f)
				_ = f.Close()
				return nil, e
			}
			return func() error { return errors.Join(release(f), f.Close()) }, nil
		}
		select {
		case <-ctx.Done():
			_ = f.Close()
			return nil, ctx.Err()
		case <-time.After(10 * time.Millisecond):
		}
	}
}

type session struct {
	d *Driver
	c *sql.Conn
}

func (d *Driver) WithSession(ctx context.Context, fn func(x.Session[Tx]) error) (err error) {
	if d.cfg.LockWaitTimeout > 0 {
		var cancel context.CancelFunc
		lockCtx, stop := context.WithTimeout(ctx, d.cfg.LockWaitTimeout)
		cancel = stop
		defer cancel()
		return d.withSession(ctx, lockCtx, fn)
	}
	return d.withSession(ctx, ctx, fn)
}

func (d *Driver) withSession(ctx, lockCtx context.Context, fn func(x.Session[Tx]) error) (err error) {
	c, e := d.db.Conn(lockCtx)
	if e != nil {
		return e
	}
	defer func() { err = errors.Join(err, c.Close()) }()
	release, e := d.lock(lockCtx, c)
	if e != nil {
		return e
	}
	defer func() { err = errors.Join(err, release()) }()
	var fk, busy int
	var journal string
	if e = c.QueryRowContext(ctx, "PRAGMA foreign_keys").Scan(&fk); e != nil {
		return e
	}
	if e = c.QueryRowContext(ctx, "PRAGMA busy_timeout").Scan(&busy); e != nil {
		return e
	}
	if e = c.QueryRowContext(ctx, "PRAGMA journal_mode").Scan(&journal); e != nil {
		return e
	}
	if strings.EqualFold(journal, "off") {
		return x.ErrUnsafeSQL
	}
	defer func() {
		cc, cancel := cleanupContext(ctx)
		defer cancel()
		_, a := c.ExecContext(cc, fmt.Sprintf("PRAGMA foreign_keys=%d", fk))
		_, b := c.ExecContext(cc, fmt.Sprintf("PRAGMA busy_timeout=%d", busy))
		err = errors.Join(err, a, b)
		if a != nil || b != nil {
			_ = c.Raw(func(any) error { return driver.ErrBadConn })
		}
	}()
	if _, e = c.ExecContext(ctx, fmt.Sprintf("PRAGMA busy_timeout=%d", busyLimit(ctx, busy, d.cfg.BusyTimeout))); e != nil {
		return e
	}
	return fn(&session{d, c})
}

func (d *Driver) ReadHistorySnapshot(ctx context.Context) (out []x.Record, err error) {
	c, e := d.db.Conn(ctx)
	if e != nil {
		return nil, e
	}
	defer func() { err = errors.Join(err, c.Close()) }()
	if _, e = c.ExecContext(ctx, "BEGIN"); e != nil {
		return nil, e
	}
	defer func() {
		cc, cancel := cleanupContext(ctx)
		defer cancel()
		_, e := c.ExecContext(cc, "ROLLBACK")
		if e != nil {
			_ = c.Raw(func(any) error { return driver.ErrBadConn })
			err = errors.Join(err, e)
		}
	}()
	s := &session{d, c}
	exists, e := s.inspect(ctx)
	if e != nil || !exists {
		return nil, e
	}
	return s.ReadHistory(ctx)
}

func (s *session) InTx(ctx context.Context, script x.Script, fn func(x.Transaction[Tx]) error) (outcome x.TxOutcome, err error) {
	fk := 1
	if script.ForeignKeys != nil && !*script.ForeignKeys {
		fk = 0
	}
	if _, err = s.c.ExecContext(ctx, fmt.Sprintf("PRAGMA foreign_keys=%d", fk)); err != nil {
		return
	}
	if _, err = s.c.ExecContext(ctx, "BEGIN IMMEDIATE"); err != nil {
		return
	}
	finished := false
	defer func() {
		cc, cancel := cleanupContext(ctx)
		defer cancel()
		if !finished {
			_, e := s.c.ExecContext(cc, "ROLLBACK")
			if e != nil {
				_ = s.c.Raw(func(any) error { return driver.ErrBadConn })
			}
			err = errors.Join(err, e)
		}
		_, e := s.c.ExecContext(cc, "PRAGMA foreign_keys=ON")
		if e != nil {
			_ = s.c.Raw(func(any) error { return driver.ErrBadConn })
		}
		err = errors.Join(err, e)
	}()
	if err = fn(&transaction{s: s}); err != nil {
		return
	}
	// A full scan is required only after deliberately disabling FK enforcement.
	// Normal transactions enforce their own changes without rejecting legacy orphans.
	if script.ForeignKeys != nil && !*script.ForeignKeys {
		rows, e := s.c.QueryContext(ctx, "PRAGMA foreign_key_check")
		if e != nil {
			err = e
			return
		}
		bad := rows.Next()
		err = errors.Join(rows.Err(), rows.Close())
		if err != nil {
			return
		}
		if bad {
			err = x.ErrForeignKey
			return
		}
	}
	if _, err = s.c.ExecContext(ctx, "COMMIT"); err != nil {
		cc, cancel := cleanupContext(ctx)
		defer cancel()
		_, rollbackErr := s.c.ExecContext(cc, "ROLLBACK")
		finished = true
		if rollbackErr == nil {
			return x.TxNotCommitted, err
		}
		_ = s.c.Raw(func(any) error { return driver.ErrBadConn })
		outcome = x.TxUnknown
		err = errors.Join(x.ErrCommitOutcomeUnknown, err, rollbackErr)
		return
	}
	finished = true
	outcome = x.TxCommitted
	return
}

type transaction struct{ s *session }

func (t *transaction) Executor() Tx { return executor{t.s.c} }
func (t *transaction) ExecScript(ctx context.Context, s x.Script) error {
	if e := t.s.d.ValidateScript(s); e != nil {
		return e
	}
	_, e := t.s.c.ExecContext(ctx, s.SQL)
	return e
}

func (t *transaction) ReadHistory(ctx context.Context) ([]x.Record, error) {
	return t.s.ReadHistory(ctx)
}

func (t *transaction) Insert(ctx context.Context, r x.Record) error {
	_, e := t.s.c.ExecContext(ctx, "INSERT INTO "+t.s.d.table("history")+"(version,name,up_checksum,down_checksum,down_kind,up_hook_revision,down_hook_revision,applied_at) VALUES(?,?,?,?,?,?,?,?)", r.Version, r.Name, r.UpChecksum[:], r.DownChecksum[:], r.DownKind, r.UpHookRevision, r.DownHookRevision, r.AppliedAt.UTC().Format(time.RFC3339Nano))
	return e
}

func (t *transaction) Delete(ctx context.Context, v x.Version) error {
	return t.change(ctx, "DELETE FROM "+t.s.d.table("history")+" WHERE version=?", v)
}

func (t *transaction) UpdateDownMetadata(ctx context.Context, v x.Version, d x.DownDefinition) error {
	return t.change(ctx, "UPDATE "+t.s.d.table("history")+" SET down_checksum=?,down_kind=?,down_hook_revision=? WHERE version=?", d.Checksum[:], d.Kind, d.HookRevision, v)
}

func (t *transaction) change(ctx context.Context, q string, args ...any) error {
	r, e := t.s.c.ExecContext(ctx, q, args...)
	if e != nil {
		return e
	}
	n, e := r.RowsAffected()
	if e != nil {
		return e
	}
	if n != 1 {
		return x.ErrHistoryConflict
	}
	return nil
}

func (*Driver) SQLDialect() string { return "sqlite" }

// Preserve the borrowed connection's timeout unless an override was requested,
// then cap the effective wait by the caller's remaining deadline.
func busyLimit(ctx context.Context, inherited int, configured time.Duration) int64 {
	limit := int64(inherited)
	if configured > 0 {
		limit = configured.Milliseconds()
	}
	if deadline, ok := ctx.Deadline(); ok && limit > 0 {
		limit = min(limit, max(int64(0), time.Until(deadline).Milliseconds()))
	}
	return limit
}
