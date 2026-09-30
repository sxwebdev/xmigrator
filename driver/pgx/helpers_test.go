package pgx

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	x "github.com/sxwebdev/xmigrator"
	"github.com/sxwebdev/xmigrator/internal/metadata"
)

var fault = errors.New("injected pgx boundary failure")

type fakeConnection struct {
	unsupported bool

	missingSuffix     string
	onLock            func()
	dropRollbackError bool

	exists                                                   bool
	kind, owner                                              string
	format, id, count                                        int
	queryFail, execFail, scanFail                            string
	columnsBad, constraintsBad, altered, extension, rowsFail bool
	beginErr, closeErr, rollbackErr, commitErr               error
	state                                                    byte
	lockBusy                                                 int
	unlock                                                   bool
	history                                                  [][]any
	affected                                                 int64
	drops                                                    int
	inspectRows                                              int
}

func connectionFor(t *testing.T) (*Driver, *fakeConnection) {
	t.Helper()
	c := &fakeConnection{kind: "r", owner: metadata.Owner, format: metadata.Format, id: 1, count: 1, state: 'T', unlock: true, affected: 1}
	d, e := newDriver(Config{DropSchemas: []string{"app"}}, func(context.Context) (connection, error) { return c, nil })
	if e != nil {
		t.Fatal(e)
	}
	return d, c
}
func (c *fakeConnection) TxStatus() byte              { return c.state }
func (c *fakeConnection) Close(context.Context) error { return c.closeErr }
func (c *fakeConnection) Begin(context.Context) (pgx.Tx, error) {
	if c.beginErr != nil {
		return nil, c.beginErr
	}
	c.state = 'T'
	return &fakeTransaction{c: c}, nil
}

func (c *fakeConnection) BeginTx(ctx context.Context, _ pgx.TxOptions) (pgx.Tx, error) {
	return c.Begin(ctx)
}

func (c *fakeConnection) QueryRow(_ context.Context, q string, args ...any) pgx.Row {
	if c.queryFail != "" && strings.Contains(q, c.queryFail) {
		return fakeRow{err: fault}
	}
	if c.scanFail != "" && strings.Contains(q, c.scanFail) {
		return fakeRow{values: []any{struct{}{}}}
	}
	switch {
	case strings.Contains(q, "pg_try_advisory_lock"):
		if c.onLock != nil {
			c.onLock()
		}
		if c.lockBusy > 0 {
			c.lockBusy--
			return fakeRow{values: []any{false}}
		}
		return fakeRow{values: []any{true}}
	case strings.Contains(q, "pg_advisory_unlock"):
		return fakeRow{values: []any{c.unlock}}
	case strings.Contains(q, "c.relkind::text,c.oid"):
		if !c.exists || c.missingSuffix != "" && strings.HasSuffix(args[1].(string), c.missingSuffix) {
			return fakeRow{err: pgx.ErrNoRows}
		}
		oid := uint32(1)
		if strings.HasSuffix(args[1].(string), "history") {
			oid = 2
		}
		return fakeRow{values: []any{c.kind, oid}}
	case strings.Contains(q, "relrowsecurity"):
		return fakeRow{values: []any{c.altered}}
	case strings.Contains(q, "SELECT count(*)"):
		return fakeRow{values: []any{c.count}}
	case strings.Contains(q, "id,owner_id"):
		return fakeRow{values: []any{c.id, c.owner, c.format}}
	case strings.Contains(q, "pg_operator"):
		return fakeRow{values: []any{c.unsupported}}
	case strings.Contains(q, "SELECT EXISTS"):
		return fakeRow{values: []any{c.extension}}
	}
	return fakeRow{err: fault}
}

func (c *fakeConnection) Query(_ context.Context, q string, args ...any) (pgx.Rows, error) {
	if c.queryFail != "" && strings.Contains(q, c.queryFail) {
		return nil, fault
	}
	var values [][]any
	switch {
	case strings.Contains(q, "pg_attribute"):
		if args[0].(uint32) == 1 {
			values = [][]any{{"id", "integer", true, ""}, {"owner_id", "text", true, ""}, {"format_version", "integer", true, ""}}
		} else {
			values = [][]any{{"version", "bigint", true, ""}, {"name", "text", true, ""}, {"up_checksum", "bytea", true, ""}, {"down_checksum", "bytea", true, ""}, {"down_kind", "text", true, ""}, {"up_hook_revision", "text", true, ""}, {"down_hook_revision", "text", true, ""}, {"applied_at", "timestamp with time zone", true, ""}, {"apply_order", "bigint", true, "a"}}
		}
		if c.columnsBad {
			values = values[:1]
		}
	case strings.Contains(q, "pg_get_constraintdef"):
		if args[0].(uint32) == 1 {
			values = [][]any{{"CHECK ((id = 1))"}, {"PRIMARY KEY (id)"}}
		} else {
			values = [][]any{{"CHECK ((down_kind = ANY (ARRAY['sql'::text, 'noop'::text, 'irreversible'::text])))"}, {"CHECK ((octet_length(down_checksum) = 32))"}, {"CHECK ((octet_length(up_checksum) = 32))"}, {"CHECK ((version > 0))"}, {"PRIMARY KEY (version)"}, {"UNIQUE (apply_order)"}}
		}
		if c.constraintsBad {
			values = values[:1]
		}
	case strings.Contains(q, "SELECT version"):
		values = c.history
	case strings.Contains(q, "UNION ALL SELECT"):
		values = [][]any{{"r", "app.users"}, {"p", "app.parts"}, {"f", "app.remote"}, {"v", "app.view"}, {"m", "app.mv"}, {"S", "app.seq"}, {"function", "app.f()"}, {"procedure", "app.p()"}, {"type", "app.kind"}, {"domain", "app.identifier"}, {"aggregate", "app.s(integer)"}}
	}
	if c.scanFail != "" && strings.Contains(q, c.scanFail) {
		values = [][]any{{struct{}{}}}
	}
	return &fakeRows{values: values, fail: c.rowsFail}, nil
}

func (c *fakeConnection) Exec(_ context.Context, q string, args ...any) (pgconn.CommandTag, error) {
	if c.dropRollbackError && (strings.HasPrefix(q, "DROP") || strings.HasPrefix(q, "ROLLBACK TO")) {
		return pgconn.CommandTag{}, fault
	}
	if c.execFail != "" && strings.Contains(q, c.execFail) {
		return pgconn.CommandTag{}, fault
	}
	if strings.HasPrefix(q, "DROP") {
		c.drops++
	}
	return pgconn.NewCommandTag("UPDATE " + string(rune('0'+c.affected))), nil
}

type fakeTransaction struct {
	pgx.Tx
	c *fakeConnection
}

func (t *fakeTransaction) Exec(ctx context.Context, q string, args ...any) (pgconn.CommandTag, error) {
	return t.c.Exec(ctx, q, args...)
}

func (t *fakeTransaction) Query(ctx context.Context, q string, args ...any) (pgx.Rows, error) {
	return t.c.Query(ctx, q, args...)
}

func (t *fakeTransaction) QueryRow(ctx context.Context, q string, args ...any) pgx.Row {
	return t.c.QueryRow(ctx, q, args...)
}

func (t *fakeTransaction) Commit(context.Context) error {
	if t.c.commitErr != nil {
		return t.c.commitErr
	}
	t.c.state = 'I'
	return nil
}

func (t *fakeTransaction) Rollback(context.Context) error {
	if t.c.rollbackErr != nil {
		return t.c.rollbackErr
	}
	if t.c.state == 'I' {
		return pgx.ErrTxClosed
	}
	t.c.state = 'I'
	return nil
}

type fakeRow struct {
	values []any
	err    error
}

func (r fakeRow) Scan(dest ...any) error {
	if r.err != nil {
		return r.err
	}
	if len(dest) != len(r.values) {
		return fault
	}
	for i, d := range dest {
		to := reflect.ValueOf(d).Elem()
		from := reflect.ValueOf(r.values[i])
		if !from.Type().ConvertibleTo(to.Type()) {
			return fault
		}
		to.Set(from.Convert(to.Type()))
	}
	return nil
}

type fakeRows struct {
	pgx.Rows
	values [][]any
	index  int
	fail   bool
}

func (r *fakeRows) Close() {}
func (r *fakeRows) Next() bool {
	if r.index < len(r.values) {
		r.index++
		return true
	}
	return false
}

func (r *fakeRows) Scan(dest ...any) error {
	return (fakeRow{values: r.values[r.index-1]}).Scan(dest...)
}

func (r *fakeRows) Err() error {
	if r.fail {
		return fault
	}
	return nil
}

func recordValues() [][]any {
	return [][]any{{int64(1), "users", make([]byte, 32), make([]byte, 32), "sql", "", "", time.Now().UTC(), int64(1)}}
}

var _ x.Driver[pgx.Tx] = (*Driver)(nil)
