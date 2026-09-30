// Package pgx implements xmigrator with native pgx v5 transactions.
package pgx

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
	x "github.com/sxwebdev/xmigrator"
	"github.com/sxwebdev/xmigrator/internal/metadata"
)

type Config struct {
	MetadataSchema, MetadataPrefix                    string
	RequireExistingMetadata                           bool
	DropSchemas                                       []string
	LockWaitTimeout, DDLLockTimeout, StatementTimeout time.Duration
}
type Driver struct {
	cfg     Config
	prefix  string
	acquire func(context.Context) (connection, error)
	lockKey int64
}

func newDriver(cfg Config, acquire func(context.Context) (connection, error)) (*Driver, error) {
	if cfg.MetadataSchema == "" {
		cfg.MetadataSchema = "xmigrator"
	}
	if !metadata.Identifier(cfg.MetadataSchema) || systemSchema(cfg.MetadataSchema) || cfg.LockWaitTimeout < 0 || cfg.DDLLockTimeout < 0 || cfg.StatementTimeout < 0 || cfg.DDLLockTimeout%time.Millisecond != 0 || cfg.StatementTimeout%time.Millisecond != 0 {
		return nil, x.ErrInvalidConfig
	}
	p, e := metadata.Prefix(cfg.MetadataPrefix)
	if e != nil {
		return nil, e
	}
	seen := map[string]bool{}
	for _, s := range cfg.DropSchemas {
		if !metadata.Identifier(s) || systemSchema(s) || s == cfg.MetadataSchema || seen[s] {
			return nil, x.ErrDropScope
		}
		seen[s] = true
	}
	cfg.DropSchemas = append([]string(nil), cfg.DropSchemas...)
	hash := sha256.Sum256([]byte(metadata.Owner + ":" + cfg.MetadataSchema))
	return &Driver{cfg, p, acquire, int64(binary.BigEndian.Uint64(hash[:8]))}, nil
}
func systemSchema(s string) bool { return s == "information_schema" || strings.HasPrefix(s, "pg_") }

// FromPool preserves the application's dialer. Every run hijacks and physically closes one connection.
// The pool itself remains owned by the caller.
func FromPool(pool *pgxpool.Pool, cfg Config) (*Driver, error) {
	if pool == nil {
		return nil, x.ErrInvalidConfig
	}
	return newDriver(cfg, func(ctx context.Context) (connection, error) {
		c, e := pool.Acquire(ctx)
		if e != nil {
			return nil, e
		}
		return nativeConnection{c.Hijack()}, nil
	})
}

func FromConfig(conn *pgx.ConnConfig, cfg Config) (*Driver, error) {
	if conn == nil {
		return nil, x.ErrInvalidConfig
	}
	copied := conn.Copy()
	return newDriver(cfg, func(ctx context.Context) (connection, error) {
		c, e := pgx.ConnectConfig(ctx, copied.Copy())
		if e != nil {
			return nil, e
		}
		return nativeConnection{c}, nil
	})
}

func FromDSN(dsn string, cfg Config) (*Driver, error) {
	conn, e := pgx.ParseConfig(dsn)
	if e != nil {
		return nil, e
	}
	return FromConfig(conn, cfg)
}
func (d *Driver) ValidateScript(s x.Script) error { return x.ValidateSQL(s, "pgx") }
func ident(parts ...string) string                { return pgx.Identifier(parts).Sanitize() }
func (d *Driver) table(s string) string           { return ident(d.cfg.MetadataSchema, d.prefix+s) }
func cleanupContext(ctx context.Context) (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
}

type connection interface {
	queryer
	Begin(context.Context) (pgx.Tx, error)
	BeginTx(context.Context, pgx.TxOptions) (pgx.Tx, error)
	Close(context.Context) error
	TxStatus() byte
}
type nativeConnection struct{ *pgx.Conn }

func (c nativeConnection) TxStatus() byte { return c.PgConn().TxStatus() }

type session struct {
	d *Driver
	c connection
}

func (d *Driver) WithSession(ctx context.Context, fn func(x.Session[pgx.Tx]) error) (err error) {
	lockCtx := ctx
	if d.cfg.LockWaitTimeout > 0 {
		var stop context.CancelFunc
		lockCtx, stop = context.WithTimeout(ctx, d.cfg.LockWaitTimeout)
		defer stop()
	}
	c, e := d.acquire(lockCtx)
	if e != nil {
		return e
	}
	defer func() { cc, stop := cleanupContext(ctx); defer stop(); err = errors.Join(err, c.Close(cc)) }()
	delay := 10 * time.Millisecond
	for {
		if e = lockCtx.Err(); e != nil {
			return e
		}
		var ok bool
		if e = c.QueryRow(lockCtx, "SELECT pg_try_advisory_lock($1)", d.lockKey).Scan(&ok); e != nil {
			return e
		}
		if ok {
			break
		}
		select {
		case <-lockCtx.Done():
			return lockCtx.Err()
		case <-time.After(delay):
			delay = min(delay*2, 500*time.Millisecond)
		}
	}
	defer func() {
		cc, stop := cleanupContext(ctx)
		defer stop()
		var ok bool
		e := c.QueryRow(cc, "SELECT pg_advisory_unlock($1)", d.lockKey).Scan(&ok)
		if e == nil && !ok {
			e = x.ErrHistoryConflict
		}
		err = errors.Join(err, e)
	}()
	if e = lockCtx.Err(); e != nil {
		return e
	}
	return fn(&session{d, c})
}

func (d *Driver) ReadHistorySnapshot(ctx context.Context) (out []x.Record, err error) {
	c, e := d.acquire(ctx)
	if e != nil {
		return nil, e
	}
	defer func() { cc, stop := cleanupContext(ctx); defer stop(); err = errors.Join(err, c.Close(cc)) }()
	tx, e := c.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly})
	if e != nil {
		return nil, e
	}
	defer func() {
		cc, stop := cleanupContext(ctx)
		defer stop()
		e := tx.Rollback(cc)
		if !errors.Is(e, pgx.ErrTxClosed) {
			err = errors.Join(err, e)
		}
	}()
	exists, e := d.inspect(ctx, tx)
	if e != nil || !exists {
		return nil, e
	}
	return d.readHistory(ctx, tx)
}

func (s *session) InTx(ctx context.Context, script x.Script, fn func(x.Transaction[pgx.Tx]) error) (outcome x.TxOutcome, err error) {
	tx, e := s.c.Begin(ctx)
	if e != nil {
		return x.TxNotCommitted, e
	}
	defer func() {
		cc, stop := cleanupContext(ctx)
		defer stop()
		e := tx.Rollback(cc)
		if !errors.Is(e, pgx.ErrTxClosed) {
			err = errors.Join(err, e)
		}
	}()
	if _, err = tx.Exec(ctx, "SELECT set_config('lock_timeout',COALESCE(NULLIF($1,'0ms'),current_setting('lock_timeout')),true),set_config('statement_timeout',COALESCE(NULLIF($2,'0ms'),current_setting('statement_timeout')),true),set_config('standard_conforming_strings','on',true)", fmt.Sprintf("%dms", s.d.cfg.DDLLockTimeout.Milliseconds()), fmt.Sprintf("%dms", s.d.cfg.StatementTimeout.Milliseconds())); err != nil {
		return
	}
	if err = fn(&transaction{s, tx}); err != nil {
		return
	}
	if s.c.TxStatus() != 'T' {
		err = x.ErrUnsafeSQL
		return
	}
	err = tx.Commit(ctx)
	if err != nil {
		if pgerr, serverError := errors.AsType[*pgconn.PgError](err); errors.Is(err, pgx.ErrTxCommitRollback) || serverError && pgerr.SeverityUnlocalized == "ERROR" {
			outcome = x.TxNotCommitted
		} else {
			outcome = x.TxUnknown
			err = errors.Join(x.ErrCommitOutcomeUnknown, err)
		}
		return
	}
	outcome = x.TxCommitted
	return
}

type transaction struct {
	s  *session
	tx pgx.Tx
}

func (t *transaction) Executor() pgx.Tx { return t.tx }
func (t *transaction) ExecScript(ctx context.Context, s x.Script) error {
	if e := t.s.d.ValidateScript(s); e != nil {
		return e
	}
	_, e := t.tx.Exec(ctx, s.SQL)
	return e
}

func (t *transaction) ReadHistory(ctx context.Context) ([]x.Record, error) {
	return t.s.d.readHistory(ctx, t.tx)
}

func (t *transaction) Insert(ctx context.Context, r x.Record) error {
	_, e := t.tx.Exec(ctx, "INSERT INTO "+t.s.d.table("history")+"(version,name,up_checksum,down_checksum,down_kind,up_hook_revision,down_hook_revision,applied_at) VALUES($1,$2,$3,$4,$5,$6,$7,$8)", r.Version, r.Name, r.UpChecksum[:], r.DownChecksum[:], r.DownKind, r.UpHookRevision, r.DownHookRevision, r.AppliedAt)
	return e
}

func (t *transaction) Delete(ctx context.Context, v x.Version) error {
	tag, e := t.tx.Exec(ctx, "DELETE FROM "+t.s.d.table("history")+" WHERE version=$1", v)
	if e == nil && tag.RowsAffected() != 1 {
		return x.ErrHistoryConflict
	}
	return e
}

func (t *transaction) UpdateDownMetadata(ctx context.Context, v x.Version, d x.DownDefinition) error {
	tag, e := t.tx.Exec(ctx, "UPDATE "+t.s.d.table("history")+" SET down_checksum=$1,down_kind=$2,down_hook_revision=$3 WHERE version=$4", d.Checksum[:], d.Kind, d.HookRevision, v)
	if e == nil && tag.RowsAffected() != 1 {
		return x.ErrHistoryConflict
	}
	return e
}

func (s *session) ReadHistory(ctx context.Context) ([]x.Record, error) {
	return s.d.readHistory(ctx, s.c)
}

func (*Driver) SQLDialect() string { return "pgx" }

func (s *session) ReadExistingHistory(ctx context.Context) ([]x.Record, error) {
	exists, err := s.d.inspect(ctx, s.c)
	if err != nil || !exists {
		return nil, err
	}
	return s.ReadHistory(ctx)
}
