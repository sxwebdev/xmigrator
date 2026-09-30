package xmigrator_test

import (
	"context"
	"errors"
	"sync"
	"testing"
	"testing/fstest"

	x "github.com/sxwebdev/xmigrator"
	"github.com/sxwebdev/xmigrator/internal/contracttest"
)

var boundaryFailure = errors.New("boundary failed")

type fakeDriver struct {
	mu        sync.Mutex
	records   []x.Record
	order     int64
	rows      map[string]int
	fail      string
	outcome   x.TxOutcome
	sessions  int
	txEdit    func(*fakeDriver)
	silent    bool
	afterRead func()
}

func fake(t *testing.T) *fakeDriver {
	t.Helper()
	return &fakeDriver{rows: map[string]int{}, outcome: x.TxCommitted}
}

func (d *fakeDriver) ValidateScript(s x.Script) error {
	if d.fail == "validate" {
		return boundaryFailure
	}
	return x.ValidateSQL(s, "pgx")
}

func (d *fakeDriver) ReadHistorySnapshot(context.Context) ([]x.Record, error) {
	if d.fail == "snapshot" {
		return nil, boundaryFailure
	}
	return append([]x.Record(nil), d.records...), nil
}

func (d *fakeDriver) WithSession(ctx context.Context, fn func(x.Session[*fakeDriver]) error) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.sessions++
	if d.fail == "session" {
		return boundaryFailure
	}
	e := fn(d)
	if d.fail == "cleanup" {
		return errors.Join(e, boundaryFailure)
	}
	return e
}

func (d *fakeDriver) EnsureMetadata(context.Context) error {
	if d.fail == "ensure" {
		return boundaryFailure
	}
	return nil
}

func (d *fakeDriver) ReadHistory(context.Context) ([]x.Record, error) {
	if d.fail == "read" {
		return nil, boundaryFailure
	}
	if d.afterRead != nil {
		d.afterRead()
	}
	return append([]x.Record(nil), d.records...), nil
}

func (d *fakeDriver) InTx(ctx context.Context, s x.Script, fn func(x.Transaction[*fakeDriver]) error) (x.TxOutcome, error) {
	if d.fail == "begin" {
		return x.TxNotCommitted, boundaryFailure
	}
	if d.fail == "txread" {
		return x.TxNotCommitted, fn(&fakeTx{d})
	}
	saved := append([]x.Record(nil), d.records...)
	if d.txEdit != nil {
		d.txEdit(d)
	}
	e := fn(&fakeTx{d})
	if e != nil || d.fail == "commit" || d.fail == "commit_wrapped" || d.outcome != x.TxCommitted {
		d.records = saved
		if e == nil && !d.silent {
			e = boundaryFailure
		}
		if d.fail == "commit_wrapped" {
			return x.TxUnknown, errors.Join(x.ErrCommitOutcomeUnknown, boundaryFailure)
		}
		return d.outcomeIfFailed(), e
	}
	if d.fail == "txcleanup" {
		return x.TxCommitted, boundaryFailure
	}
	return x.TxCommitted, nil
}

func (d *fakeDriver) outcomeIfFailed() x.TxOutcome {
	if d.outcome == x.TxUnknown {
		return x.TxUnknown
	}
	return x.TxNotCommitted
}

func (d *fakeDriver) Drop(context.Context) error {
	if d.fail == "drop" {
		return boundaryFailure
	}
	d.records = nil
	return nil
}

type fakeTx struct{ d *fakeDriver }

func (t *fakeTx) Executor() *fakeDriver { return t.d }
func (t *fakeTx) ExecScript(context.Context, x.Script) error {
	if t.d.fail == "exec" {
		return boundaryFailure
	}
	return nil
}

func (t *fakeTx) ReadHistory(ctx context.Context) ([]x.Record, error) {
	if t.d.fail == "txread" {
		return nil, boundaryFailure
	}
	return t.d.ReadHistory(ctx)
}

func (t *fakeTx) Insert(_ context.Context, r x.Record) error {
	if t.d.fail == "insert" {
		return boundaryFailure
	}
	t.d.order++
	r.ApplyOrder = t.d.order
	t.d.records = append(t.d.records, r)
	return nil
}

func (t *fakeTx) Delete(_ context.Context, v x.Version) error {
	if t.d.fail == "delete" {
		return boundaryFailure
	}
	for i, r := range t.d.records {
		if r.Version == v {
			t.d.records = append(t.d.records[:i], t.d.records[i+1:]...)
			return nil
		}
	}
	return x.ErrHistoryConflict
}

func (t *fakeTx) UpdateDownMetadata(_ context.Context, v x.Version, d x.DownDefinition) error {
	if t.d.fail == "update" {
		return boundaryFailure
	}
	for i := range t.d.records {
		if t.d.records[i].Version == v {
			r := &t.d.records[i]
			r.DownChecksum = d.Checksum
			r.DownKind = d.Kind
			r.DownHookRevision = d.HookRevision
			return nil
		}
	}
	return x.ErrHistoryConflict
}

func makeMigrator(t *testing.T, d *fakeDriver, files fstest.MapFS, opts ...x.Option[*fakeDriver]) *x.Migrator[*fakeDriver] {
	t.Helper()
	s, e := x.NewSource(files, ".")
	if e != nil {
		t.Fatal(e)
	}
	m, e := x.New(s, d, opts...)
	if e != nil {
		t.Fatal(e)
	}
	return m
}

func validFiles() fstest.MapFS {
	files := fstest.MapFS{}
	contracttest.Pair(files, 1, "SELECT 1;", "SELECT 2;")
	return files
}

func (d *fakeDriver) ReadExistingHistory(ctx context.Context) ([]x.Record, error) {
	return d.ReadHistory(ctx)
}
