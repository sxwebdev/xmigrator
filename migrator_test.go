package xmigrator_test

import (
	"context"
	"errors"
	"testing"
	"testing/fstest"

	x "github.com/sxwebdev/xmigrator"
	"github.com/sxwebdev/xmigrator/internal/contracttest"
)

func TestStepsAndOrdering(t *testing.T) {
	d := fake(t)
	files := fstest.MapFS{}
	for _, v := range []int{300, 100} {
		contracttest.Pair(files, v, "SELECT 1;", "SELECT 2;")
	}
	m := makeMigrator(t, d, files)
	for _, n := range []int{-1, 0} {
		if _, e := m.Down(t.Context(), n); !errors.Is(e, x.ErrInvalidSteps) {
			t.Fatal(e)
		}
	}
	if _, e := m.Up(t.Context(), -1); !errors.Is(e, x.ErrInvalidSteps) {
		t.Fatal(e)
	}
	if d.sessions != 0 {
		t.Fatal("invalid steps connected")
	}
	r, e := m.Up(t.Context(), 1)
	if e != nil || len(r.Actions) != 1 || r.Actions[0].Version != 100 {
		t.Fatalf("first step %+v %v", r, e)
	}
	r, e = m.Up(t.Context(), 99)
	if e != nil || len(r.Actions) != 1 || r.Actions[0].Version != 300 {
		t.Fatalf("rest %+v %v", r, e)
	}
	contracttest.Pair(files, 200, "SELECT 3;", "SELECT 4;")
	r, e = m.Up(t.Context(), 0)
	if e != nil || len(r.Actions) != 1 || r.Actions[0].Version != 200 || !r.Actions[0].OutOfOrder {
		t.Fatalf("late %+v %v", r, e)
	}
	r, e = m.Down(t.Context(), 1)
	if e != nil || r.Actions[0].Version != 200 {
		t.Fatalf("down %+v %v", r, e)
	}
	r, e = m.DownAll(t.Context())
	if e != nil || len(r.Actions) != 2 || r.Actions[0].Version != 300 || r.Actions[1].Version != 100 {
		t.Fatalf("downall %+v %v", r, e)
	}
}

func TestNewOptions(t *testing.T) {
	s, _ := x.NewSource(validFiles(), ".")
	d := fake(t)
	for _, opts := range [][]x.Option[*fakeDriver]{{nil}, {x.WithChecksumPolicy[*fakeDriver](99)}, {x.WithUnknownAppliedPolicy[*fakeDriver](99)}, {x.WithHooks(map[x.Version]x.Hooks[*fakeDriver]{0: {}})}, {x.WithHooks(map[x.Version]x.Hooks[*fakeDriver]{1: {}}), x.WithHooks(map[x.Version]x.Hooks[*fakeDriver]{1: {}})}} {
		if _, e := x.New(s, d, opts...); !errors.Is(e, x.ErrInvalidConfig) {
			t.Fatal(e)
		}
	}
	if _, e := x.New[*fakeDriver](x.Source{}, d); !errors.Is(e, x.ErrInvalidConfig) {
		t.Fatal(e)
	}
	var nilDriver *fakeDriver
	if _, e := x.New(s, nilDriver); !errors.Is(e, x.ErrInvalidConfig) {
		t.Fatal(e)
	}
}

func TestHooks(t *testing.T) {
	d := fake(t)
	files := validFiles()
	files["1_m1.up.sql"].Data = []byte("-- xmigrator:hooks=u1\nSELECT 1;")
	files["1_m1.down.sql"].Data = []byte("-- xmigrator:hooks=d1\nSELECT 2;")
	var calls []string
	callback := func(name string) func(context.Context, *fakeDriver) error {
		return func(context.Context, *fakeDriver) error { calls = append(calls, name); return nil }
	}
	hooks := map[x.Version]x.Hooks[*fakeDriver]{1: {UpRevision: "u1", DownRevision: "d1", BeforeUp: callback("before up"), AfterUp: callback("after up"), BeforeDown: callback("before down"), AfterDown: callback("after down")}}
	m := makeMigrator(t, d, files, x.WithHooks(hooks))
	delete(hooks, 1)
	if _, e := m.Up(t.Context(), 0); e != nil {
		t.Fatal(e)
	}
	if _, e := m.DownAll(t.Context()); e != nil {
		t.Fatal(e)
	}
	want := []string{"before up", "after up", "before down", "after down"}
	for i, n := range want {
		if len(calls) != 4 || calls[i] != n {
			t.Fatalf("hook effects: %v", calls)
		}
	}
	for _, opts := range [][]x.Option[*fakeDriver]{nil, {x.WithHooks(map[x.Version]x.Hooks[*fakeDriver]{2: {}})}, {x.WithHooks(map[x.Version]x.Hooks[*fakeDriver]{1: {UpRevision: "u1", DownRevision: "d1"}})}} {
		bad := makeMigrator(t, d, files, opts...)
		if _, e := bad.Up(t.Context(), 0); !errors.Is(e, x.ErrHookMismatch) {
			t.Fatal(e)
		}
	}
	blank := validFiles()
	orphan := makeMigrator(t, d, blank, x.WithHooks(map[x.Version]x.Hooks[*fakeDriver]{2: {}}))
	if _, e := orphan.Up(t.Context(), 0); !errors.Is(e, x.ErrHookMismatch) {
		t.Fatal(e)
	}
}

func TestPoliciesAndRepair(t *testing.T) {
	for _, p := range []x.ChecksumPolicy{x.ChecksumStrict, x.ChecksumWarn, x.ChecksumDisabled} {
		t.Run(string(rune('0'+p)), func(t *testing.T) {
			t.Parallel()
			d := fake(t)
			files := validFiles()
			m := makeMigrator(t, d, files, x.WithChecksumPolicy[*fakeDriver](p))
			if _, e := m.Up(t.Context(), 0); e != nil {
				t.Fatal(e)
			}
			original := d.records[0]
			files["1_m1.up.sql"].Data = []byte("SELECT 1; -- changed\n")
			files["1_m1.down.sql"].Data = []byte("SELECT 22;")
			r, e := m.Up(t.Context(), 0)
			if p == x.ChecksumStrict {
				if !errors.Is(e, x.ErrChecksumMismatch) {
					t.Fatal(e)
				}
			} else if e != nil {
				t.Fatal(e)
			}
			if p == x.ChecksumDisabled && len(r.Issues) != 0 {
				t.Fatal("disabled compared checksum")
			}
			status, e := m.Status(t.Context())
			if e != nil {
				t.Fatal(e)
			}
			state := "mismatch"
			if p == x.ChecksumDisabled {
				state = "not_checked"
			}
			if status.Migrations[0].ChecksumState != state {
				t.Fatal(status)
			}
			preview, e := m.PlanRepairDown(t.Context(), 1)
			if e != nil || !preview.WouldChange {
				t.Fatalf("preview %+v %v", preview, e)
			}
			if d.records[0] != original {
				t.Fatal("preview wrote history")
			}
			repair, e := m.RepairDown(t.Context(), 1)
			if e != nil || !repair.Changed || !repair.Confirmed {
				t.Fatalf("repair %+v %v", repair, e)
			}
			if d.records[0].UpChecksum != original.UpChecksum {
				t.Fatal("repair rewrote up")
			}
			repair, e = m.RepairDown(t.Context(), 1)
			if e != nil || repair.Changed || !repair.Confirmed {
				t.Fatalf("no-op %+v %v", repair, e)
			}
			_, e = m.DownAll(t.Context())
			if p == x.ChecksumStrict {
				if !errors.Is(e, x.ErrChecksumMismatch) {
					t.Fatal(e)
				}
			} else if e != nil {
				t.Fatal(e)
			}
		})
	}
}

func TestDefinitionValidation(t *testing.T) {
	for _, tt := range []struct {
		name     string
		change   func(*x.Record)
		up, down error
	}{
		{"name", func(r *x.Record) { r.Name = "other" }, x.ErrDefinitionMismatch, x.ErrDefinitionMismatch},
		{"up_revision", func(r *x.Record) { r.UpHookRevision = "old" }, x.ErrHookMismatch, x.ErrHookMismatch},
		{"down_revision", func(r *x.Record) { r.DownHookRevision = "old" }, nil, x.ErrHookMismatch},
		{"down_kind", func(r *x.Record) { r.DownKind = x.DownNoop }, nil, x.ErrDefinitionMismatch},
		{"down_hash", func(r *x.Record) { r.DownChecksum[0]++ }, nil, x.ErrChecksumMismatch},
	} {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			d := fake(t)
			m := makeMigrator(t, d, validFiles())
			if _, e := m.Up(t.Context(), 0); e != nil {
				t.Fatal(e)
			}
			tt.change(&d.records[0])
			if _, e := m.Up(t.Context(), 0); !errors.Is(e, tt.up) {
				t.Fatalf("up %v want %v", e, tt.up)
			}
			if _, e := m.DownAll(t.Context()); !errors.Is(e, tt.down) {
				t.Fatalf("down %v want %v", e, tt.down)
			}
			if tt.name == "name" {
				if _, e := m.RepairDown(t.Context(), 1); !errors.Is(e, x.ErrDefinitionMismatch) {
					t.Fatal(e)
				}
			}
		})
	}
}

func TestUnknownAndIrreversible(t *testing.T) {
	d := fake(t)
	files := validFiles()
	m := makeMigrator(t, d, files)
	if _, e := m.Up(t.Context(), 0); e != nil {
		t.Fatal(e)
	}
	delete(files, "1_m1.up.sql")
	delete(files, "1_m1.down.sql")
	r, e := m.Up(t.Context(), 0)
	if e != nil || len(r.Issues) != 1 {
		t.Fatalf("unknown allow %+v %v", r, e)
	}
	status, e := m.Status(t.Context())
	if e != nil || status.Migrations[0].State != "unknown_applied" {
		t.Fatalf("status %+v %v", status, e)
	}
	strict := makeMigrator(t, d, files, x.WithUnknownAppliedPolicy[*fakeDriver](x.UnknownAppliedError))
	if _, e := strict.Up(t.Context(), 0); !errors.Is(e, x.ErrUnknownApplied) {
		t.Fatal(e)
	}
	if _, e := m.DownAll(t.Context()); !errors.Is(e, x.ErrUnknownApplied) {
		t.Fatal(e)
	}
	files = validFiles()
	files["1_m1.down.sql"].Data = []byte("-- xmigrator:irreversible")
	d = fake(t)
	m = makeMigrator(t, d, files)
	if _, e := m.Up(t.Context(), 0); e != nil {
		t.Fatal(e)
	}
	if _, e := m.DownAll(t.Context()); !errors.Is(e, x.ErrIrreversible) {
		t.Fatal(e)
	}
}

func TestBoundaryFailuresAndConfirmedEffects(t *testing.T) {
	for _, stage := range []string{"validate", "session", "ensure", "read", "begin", "txread", "exec", "insert", "commit", "txcleanup", "cleanup"} {
		t.Run(stage, func(t *testing.T) {
			t.Parallel()
			d := fake(t)
			m := makeMigrator(t, d, validFiles())
			d.fail = stage
			r, e := m.Up(t.Context(), 0)
			if !errors.Is(e, boundaryFailure) {
				t.Fatalf("%v", e)
			}
			want := 0
			if stage == "txcleanup" || stage == "cleanup" {
				want = 1
			}
			if len(d.records) != want || len(r.Actions) != want {
				t.Fatalf("confirmed effects: records=%d actions=%d want=%d", len(d.records), len(r.Actions), want)
			}
			d.fail = ""
			if _, e = m.Up(t.Context(), 0); e != nil {
				t.Fatal("unusable after error", e)
			}
		})
	}
	for _, stage := range []string{"delete", "update"} {
		t.Run(stage, func(t *testing.T) {
			d := fake(t)
			files := validFiles()
			m := makeMigrator(t, d, files)
			if _, e := m.Up(t.Context(), 0); e != nil {
				t.Fatal(e)
			}
			d.fail = stage
			if stage == "delete" {
				if _, e := m.DownAll(t.Context()); !errors.Is(e, boundaryFailure) {
					t.Fatal(e)
				}
			} else {
				files["1_m1.down.sql"].Data = []byte("SELECT 3;")
				r, e := m.RepairDown(t.Context(), 1)
				if !errors.Is(e, boundaryFailure) || r.Confirmed {
					t.Fatalf("%+v %v", r, e)
				}
			}
			if len(d.records) != 1 {
				t.Fatal("history lost")
			}
		})
	}
	d := fake(t)
	m := makeMigrator(t, d, validFiles())
	d.outcome = x.TxUnknown
	r, e := m.Up(t.Context(), 0)
	if !errors.Is(e, x.ErrCommitOutcomeUnknown) || len(r.Actions) != 0 {
		t.Fatalf("unknown %+v %v", r, e)
	}
}

func TestRepairFailures(t *testing.T) {
	for _, stage := range []string{"validate", "snapshot", "read", "session", "begin", "txread", "commit", "txcleanup", "cleanup"} {
		t.Run(stage, func(t *testing.T) {
			t.Parallel()
			d := fake(t)
			files := validFiles()
			m := makeMigrator(t, d, files)
			if _, e := m.Up(t.Context(), 0); e != nil {
				t.Fatal(e)
			}
			files["1_m1.down.sql"].Data = []byte("SELECT 99;")
			d.fail = stage
			if stage == "snapshot" {
				if _, e := m.PlanRepairDown(t.Context(), 1); !errors.Is(e, boundaryFailure) {
					t.Fatal(e)
				}
				return
			}
			r, e := m.RepairDown(t.Context(), 1)
			if !errors.Is(e, boundaryFailure) {
				t.Fatal(e)
			}
			want := stage == "txcleanup" || stage == "cleanup"
			if r.Confirmed != want || r.Changed != want {
				t.Fatalf("repair %+v expected confirmation=%v", r, want)
			}
		})
	}
	d := fake(t)
	files := validFiles()
	m := makeMigrator(t, d, files)
	if _, e := m.Up(t.Context(), 0); e != nil {
		t.Fatal(e)
	}
	files["1_m1.down.sql"].Data = []byte("SELECT 7;")
	d.outcome = x.TxUnknown
	if r, e := m.RepairDown(t.Context(), 1); !errors.Is(e, x.ErrCommitOutcomeUnknown) || r.Confirmed {
		t.Fatalf("repair unknown %+v %v", r, e)
	}
	for _, v := range []x.Version{0, -1} {
		if _, e := m.PlanRepairDown(t.Context(), v); !errors.Is(e, x.ErrInvalidConfig) {
			t.Fatal(e)
		}
		if _, e := m.RepairDown(t.Context(), v); !errors.Is(e, x.ErrInvalidConfig) {
			t.Fatal(e)
		}
	}
	if _, e := m.PlanRepairDown(t.Context(), 99); !errors.Is(e, x.ErrInvalidSource) {
		t.Fatal(e)
	}
	empty := makeMigrator(t, fake(t), validFiles())
	if _, e := empty.PlanRepairDown(t.Context(), 1); !errors.Is(e, x.ErrNotApplied) {
		t.Fatal(e)
	}
	delete(files, "1_m1.down.sql")
	if _, e := m.RepairDown(t.Context(), 1); !errors.Is(e, x.ErrInvalidSource) {
		t.Fatal(e)
	}
}

func TestInvalidHistoryAndStatusErrors(t *testing.T) {
	for _, tt := range []struct {
		name    string
		records []x.Record
	}{
		{"zero_version", []x.Record{{ApplyOrder: 1, Name: "x", DownKind: x.DownSQL}}},
		{"zero_order", []x.Record{{Version: 1, Name: "x", DownKind: x.DownSQL}}},
		{"empty_name", []x.Record{{Version: 1, ApplyOrder: 1, DownKind: x.DownSQL}}},
		{"kind", []x.Record{{Version: 1, ApplyOrder: 1, Name: "x", DownKind: "wrong"}}},
		{"duplicate", []x.Record{{Version: 1, ApplyOrder: 1, Name: "x", DownKind: x.DownSQL}, {Version: 1, ApplyOrder: 2, Name: "x", DownKind: x.DownSQL}}},
		{"duplicate_order", []x.Record{{Version: 1, ApplyOrder: 1, Name: "x", DownKind: x.DownSQL}, {Version: 2, ApplyOrder: 1, Name: "x", DownKind: x.DownSQL}}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			d := fake(t)
			d.records = tt.records
			m := makeMigrator(t, d, validFiles())
			if _, e := m.Up(t.Context(), 0); !errors.Is(e, x.ErrMetadataConflict) {
				t.Fatal(e)
			}
			if _, e := m.Status(t.Context()); !errors.Is(e, x.ErrMetadataConflict) {
				t.Fatal(e)
			}
			if _, e := m.PlanRepairDown(t.Context(), 1); !errors.Is(e, x.ErrMetadataConflict) {
				t.Fatal(e)
			}
		})
	}
	d := fake(t)
	m := makeMigrator(t, d, validFiles())
	d.fail = "snapshot"
	if _, e := m.Status(t.Context()); !errors.Is(e, boundaryFailure) {
		t.Fatal(e)
	}
	d.fail = "validate"
	if _, e := m.Status(t.Context()); !errors.Is(e, boundaryFailure) {
		t.Fatal(e)
	}
	if _, e := m.PlanRepairDown(t.Context(), 1); !errors.Is(e, boundaryFailure) {
		t.Fatal(e)
	}
	d.fail = ""
	if _, e := m.Status(t.Context()); e != nil {
		t.Fatal(e)
	}
	for _, stage := range []string{"ensure", "drop", "cleanup"} {
		d.fail = stage
		if e := m.Drop(t.Context()); !errors.Is(e, boundaryFailure) {
			t.Fatal(e)
		}
	}
	d.fail = ""
	if e := m.Drop(t.Context()); e != nil {
		t.Fatal(e)
	}
}

func TestPartialResultAndHookErrors(t *testing.T) {
	d := fake(t)
	files := validFiles()
	contracttest.Pair(files, 2, "-- xmigrator:hooks=u\nSELECT 2;", "-- xmigrator:hooks=d\nSELECT 2;")
	failure := errors.New("callback failure")
	hooks := map[x.Version]x.Hooks[*fakeDriver]{2: {UpRevision: "u", AfterUp: func(context.Context, *fakeDriver) error { return failure }, DownRevision: "d", AfterDown: func(context.Context, *fakeDriver) error { return failure }}}
	m := makeMigrator(t, d, files, x.WithHooks(hooks))
	r, e := m.Up(t.Context(), 0)
	if !errors.Is(e, failure) || len(r.Actions) != 1 || len(d.records) != 1 {
		t.Fatalf("partial %+v %v", r, e)
	}
	hooks[2] = x.Hooks[*fakeDriver]{UpRevision: "u", BeforeUp: func(context.Context, *fakeDriver) error { return failure }, DownRevision: "d", BeforeDown: func(context.Context, *fakeDriver) error { return failure }}
	m = makeMigrator(t, d, files, x.WithHooks(hooks))
	if _, e := m.Up(t.Context(), 0); !errors.Is(e, failure) {
		t.Fatal(e)
	}
	hooks[2] = x.Hooks[*fakeDriver]{UpRevision: "u", AfterUp: func(context.Context, *fakeDriver) error { return nil }, DownRevision: "d", AfterDown: func(context.Context, *fakeDriver) error { return failure }}
	m = makeMigrator(t, d, files, x.WithHooks(hooks))
	if _, e := m.Up(t.Context(), 0); e != nil {
		t.Fatal(e)
	}
	r, e = m.Down(t.Context(), 1)
	if !errors.Is(e, failure) || len(r.Actions) != 0 || len(d.records) != 2 {
		t.Fatalf("down error %+v %v", r, e)
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, e = m.Up(ctx, 0); !errors.Is(e, context.Canceled) {
		t.Fatal(e)
	}
}

func ExampleNewSource() {
	s, _ := x.NewSource(fstest.MapFS{"1_users.up.sql": {Data: []byte("CREATE TABLE users(id INTEGER);")}, "1_users.down.sql": {Data: []byte("DROP TABLE users;")}}, ".")
	_ = s
	// Output:
}

func TestHistoryPreconditionsAndInconsistentDrivers(t *testing.T) {
	for _, direction := range []x.Direction{x.Up, x.Down} {
		t.Run(string(direction), func(t *testing.T) {
			d := fake(t)
			m := makeMigrator(t, d, validFiles())
			if direction == x.Down {
				if _, e := m.Up(t.Context(), 0); e != nil {
					t.Fatal(e)
				}
			}
			d.txEdit = func(d *fakeDriver) {
				if direction == x.Up {
					d.records = []x.Record{{Version: 1, Name: "m1", ApplyOrder: 1, DownKind: x.DownSQL}}
				} else {
					d.records[0].Name = "changed"
				}
			}
			var e error
			if direction == x.Up {
				_, e = m.Up(t.Context(), 0)
			} else {
				_, e = m.DownAll(t.Context())
			}
			if !errors.Is(e, x.ErrHistoryConflict) {
				t.Fatal(e)
			}
		})
	}
	for _, op := range []string{"up", "repair"} {
		t.Run(op+"_silent_not_committed", func(t *testing.T) {
			d := fake(t)
			files := validFiles()
			m := makeMigrator(t, d, files)
			if op == "repair" {
				if _, e := m.Up(t.Context(), 0); e != nil {
					t.Fatal(e)
				}
				files["1_m1.down.sql"].Data = []byte("SELECT 9;")
			}
			d.silent = true
			d.outcome = x.TxNotCommitted
			var e error
			if op == "up" {
				_, e = m.Up(t.Context(), 0)
			} else {
				_, e = m.RepairDown(t.Context(), 1)
			}
			if !errors.Is(e, x.ErrHistoryConflict) {
				t.Fatal(e)
			}
		})
	}
	for _, edit := range []string{"change", "remove"} {
		t.Run("repair_precondition_"+edit, func(t *testing.T) {
			d := fake(t)
			files := validFiles()
			m := makeMigrator(t, d, files)
			if _, e := m.Up(t.Context(), 0); e != nil {
				t.Fatal(e)
			}
			files["1_m1.down.sql"].Data = []byte("SELECT 9;")
			d.txEdit = func(d *fakeDriver) {
				if edit == "change" {
					d.records[0].DownChecksum[0]++
				} else {
					d.records = nil
				}
			}
			_, e := m.RepairDown(t.Context(), 1)
			want := x.ErrHistoryConflict
			if edit == "remove" {
				want = x.ErrNotApplied
			}
			if !errors.Is(e, want) {
				t.Fatal(e)
			}
		})
	}
	d := fake(t)
	files := validFiles()
	contracttest.Pair(files, 2, "SELECT 3;", "SELECT 4;")
	m := makeMigrator(t, d, files)
	if _, e := m.Up(t.Context(), 0); e != nil {
		t.Fatal(e)
	}
	if _, e := m.PlanRepairDown(t.Context(), 2); e != nil {
		t.Fatal(e)
	}
	ctx, cancel := context.WithCancel(t.Context())
	t.Cleanup(cancel)
	d.afterRead = cancel
	if _, e := m.Up(ctx, 0); e != nil {
		t.Fatal("empty plan should not execute", e)
	}
	contracttest.Pair(files, 3, "SELECT 5;", "SELECT 6;")
	ctx, cancel2 := context.WithCancel(t.Context())
	t.Cleanup(cancel2)
	d.afterRead = cancel2
	if _, e := m.Up(ctx, 0); !errors.Is(e, context.Canceled) {
		t.Fatal(e)
	}
}

func TestStatusSortingAndDriftDiagnostics(t *testing.T) {
	d := fake(t)
	files := validFiles()
	contracttest.Pair(files, 3, "SELECT 3;", "SELECT 3;")
	m := makeMigrator(t, d, files)
	if _, e := m.Up(t.Context(), 0); e != nil {
		t.Fatal(e)
	}
	d.records = append(d.records, x.Record{Version: 2, Name: "unknown", ApplyOrder: 3, DownKind: x.DownSQL})
	files["1_m1.down.sql"].Data = []byte("-- xmigrator:noop")
	d.records[0].UpHookRevision = "old"
	d.records[0].Name = "old_name"
	d.records[0].DownHookRevision = "old_down"
	contracttest.Pair(files, 4, "SELECT 4;", "SELECT 4;")
	r, e := m.Status(t.Context())
	if e != nil || len(r.Migrations) != 4 || r.Migrations[0].Version != 1 || r.Migrations[1].Version != 2 || r.Migrations[2].Version != 3 || len(r.Issues) < 5 {
		t.Fatalf("status %+v %v", r, e)
	}
}

func TestBeforeDownFailure(t *testing.T) {
	d := fake(t)
	files := validFiles()
	files["1_m1.down.sql"].Data = []byte("-- xmigrator:hooks=d1\nSELECT 2;")
	m := makeMigrator(t, d, files, x.WithHooks(map[x.Version]x.Hooks[*fakeDriver]{1: {DownRevision: "d1", BeforeDown: func(context.Context, *fakeDriver) error { return boundaryFailure }}}))
	if _, e := m.Up(t.Context(), 0); e != nil {
		t.Fatal(e)
	}
	if _, e := m.DownAll(t.Context()); !errors.Is(e, boundaryFailure) {
		t.Fatal(e)
	}
}
