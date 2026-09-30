package sqlite

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	x "github.com/sxwebdev/xmigrator"
	"github.com/sxwebdev/xmigrator/internal/metadata"
)

var (
	fault    = errors.New("injected SQLite boundary failure")
	engineID atomic.Int64
)

type fakeEngine struct {
	metadataAltered bool

	badQuery string

	path                 string
	definitions          map[string]string
	exists               bool
	kind, owner          string
	format, id, count    int
	fk, busy             int
	failQuery, failExec  string
	rowsErr, affectedErr bool
	affected             int64
	history              [][]driver.Value
	badDDL               bool
	fkBad                bool
	journal              string
	onCommit             func()
	onRollback           func()
	resetFail            bool
	failRollback         bool
}

func engine(t *testing.T) (*Driver, *fakeEngine) {
	t.Helper()
	p := filepath.Join(t.TempDir(), "fake.db")
	if e := os.WriteFile(p, nil, 0o600); e != nil {
		t.Fatal(e)
	}
	f := &fakeEngine{path: p, kind: "table", owner: metadata.Owner, format: metadata.Format, id: 1, count: 1, fk: 1, busy: 17, affected: 1, journal: "delete"}
	name := "xmigrator_fake_" + string(rune(engineID.Add(1)+64))
	sql.Register(name, f)
	db, e := sql.Open(name, "")
	if e != nil {
		t.Fatal(e)
	}
	db.SetMaxOpenConns(1)
	t.Cleanup(func() { db.Close() })
	d, e := FromDB(db, Config{})
	if e != nil {
		t.Fatal(e)
	}
	f.definitions = d.definitions()
	return d, f
}
func (f *fakeEngine) Open(string) (driver.Conn, error) { return &fakeConn{f}, nil }

type fakeConn struct{ f *fakeEngine }

func (c *fakeConn) Prepare(string) (driver.Stmt, error) { return nil, fault }
func (c *fakeConn) Close() error                        { return nil }
func (c *fakeConn) Begin() (driver.Tx, error)           { return nil, fault }
func (c *fakeConn) QueryContext(_ context.Context, q string, args []driver.NamedValue) (driver.Rows, error) {
	f := c.f
	if f.failQuery != "" && strings.Contains(q, f.failQuery) {
		return nil, fault
	}
	var values [][]driver.Value
	switch {
	case strings.Contains(q, "database_list"):
		values = [][]driver.Value{{int64(0), "main", f.path}}
	case strings.Contains(q, "foreign_key_check"):
		if f.fkBad {
			values = [][]driver.Value{{"child", int64(1), "parent", int64(0)}}
		}
	case strings.Contains(q, "foreign_keys"):
		values = [][]driver.Value{{int64(f.fk)}}
	case strings.Contains(q, "busy_timeout"):
		values = [][]driver.Value{{int64(f.busy)}}
	case strings.Contains(q, "journal_mode"):
		values = [][]driver.Value{{f.journal}}
	case strings.Contains(q, "SELECT type,sql"):
		if f.exists {
			name := args[0].Value.(string)
			ddl := f.definitions[name]
			if f.badDDL {
				ddl = "CREATE TABLE wrong (x INTEGER)"
			}
			if ddl != "" {
				values = [][]driver.Value{{f.kind, ddl}}
			}
		}
	case strings.Contains(q, "SELECT EXISTS"):
		values = [][]driver.Value{{f.metadataAltered}}
	case strings.Contains(q, "SELECT count(*)"):
		values = [][]driver.Value{{int64(f.count)}}
	case strings.Contains(q, "SELECT id,owner_id"):
		values = [][]driver.Value{{int64(f.id), f.owner, int64(f.format)}}
	case strings.Contains(q, "SELECT version"):
		values = f.history
	case strings.Contains(q, "SELECT type,name"):
		values = [][]driver.Value{{"table", "users"}}
	default:
		values = [][]driver.Value{{int64(1)}}
	}
	if f.badQuery != "" && strings.Contains(q, f.badQuery) {
		values = [][]driver.Value{{"bad"}}
	}
	cols := 1
	if len(values) > 0 {
		cols = len(values[0])
	}
	return &fakeRows{rows: values, n: cols, err: f.rowsErr}, nil
}

func (c *fakeConn) ExecContext(_ context.Context, q string, args []driver.NamedValue) (driver.Result, error) {
	f := c.f
	if f.failExec != "" && strings.Contains(q, f.failExec) {
		return nil, fault
	}
	if q == "COMMIT" && f.onCommit != nil {
		f.onCommit()
	}
	if q == "ROLLBACK" && f.failRollback {
		return nil, fault
	}
	if q == "ROLLBACK" && f.onRollback != nil {
		f.onRollback()
	}
	if f.resetFail && q == "PRAGMA foreign_keys=ON" {
		return nil, fault
	}
	if strings.HasPrefix(q, "PRAGMA foreign_keys=") {
		if strings.HasSuffix(q, "0") {
			f.fk = 0
		} else {
			f.fk = 1
		}
	}
	if strings.HasPrefix(q, "PRAGMA busy_timeout=") {
		if strings.HasSuffix(q, "17") {
			f.busy = 17
		} else {
			f.busy = 0
		}
	}
	return fakeResult{f.affected, f.affectedErr}, nil
}

type fakeResult struct {
	n   int64
	err bool
}

func (r fakeResult) LastInsertId() (int64, error) { return 0, nil }
func (r fakeResult) RowsAffected() (int64, error) {
	if r.err {
		return 0, fault
	}
	return r.n, nil
}

type fakeRows struct {
	rows     [][]driver.Value
	n, index int
	err      bool
}

func (r *fakeRows) Columns() []string { return make([]string, r.n) }
func (r *fakeRows) Close() error      { return nil }
func (r *fakeRows) Next(dest []driver.Value) error {
	if r.index < len(r.rows) {
		copy(dest, r.rows[r.index])
		r.index++
		return nil
	}
	if r.err {
		return fault
	}
	return io.EOF
}

func sessionFor(t *testing.T, d *Driver) *session {
	t.Helper()
	c, e := d.db.Conn(t.Context())
	if e != nil {
		t.Fatal(e)
	}
	t.Cleanup(func() { c.Close() })
	return &session{d, c}
}

var _ x.Driver[Tx] = (*Driver)(nil)
