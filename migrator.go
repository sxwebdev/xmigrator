package xmigrator

import (
	"cmp"
	"context"
	"errors"
	"maps"
	"slices"
	"time"
)

type Migrator[T any] struct {
	source   Source
	driver   Driver[T]
	hooks    map[Version]Hooks[T]
	checksum ChecksumPolicy
	unknown  UnknownAppliedPolicy
	logger   Logger
}
type Option[T any] func(*Migrator[T]) error

func WithHooks[T any](hooks map[Version]Hooks[T]) Option[T] {
	return func(m *Migrator[T]) error {
		for v, h := range hooks {
			if _, ok := m.hooks[v]; ok || v <= 0 {
				return ErrInvalidConfig
			}
			m.hooks[v] = h
		}
		return nil
	}
}

func WithChecksumPolicy[T any](p ChecksumPolicy) Option[T] {
	return func(m *Migrator[T]) error {
		if p > ChecksumDisabled {
			return ErrInvalidConfig
		}
		m.checksum = p
		return nil
	}
}

func WithUnknownAppliedPolicy[T any](p UnknownAppliedPolicy) Option[T] {
	return func(m *Migrator[T]) error {
		if p > UnknownAppliedError {
			return ErrInvalidConfig
		}
		m.unknown = p
		return nil
	}
}

func WithLogger[T any](l Logger) Option[T] {
	return func(m *Migrator[T]) error {
		if !isNil(l) {
			m.logger = l
		}
		return nil
	}
}

func New[T any](s Source, d Driver[T], opts ...Option[T]) (*Migrator[T], error) {
	if isNil(s.files) || isNil(d) {
		return nil, ErrInvalidConfig
	}
	m := &Migrator[T]{source: s, driver: d, hooks: map[Version]Hooks[T]{}, logger: nopLogger{}}
	for _, o := range opts {
		if o == nil {
			return nil, ErrInvalidConfig
		}
		if err := o(m); err != nil {
			return nil, err
		}
	}
	if dialect, ok := any(d).(interface{ SQLDialect() string }); ok {
		m.source.dialect = dialect.SQLDialect()
	}
	m.hooks = maps.Clone(m.hooks)
	return m, nil
}

func (m *Migrator[T]) snapshot(ctx context.Context, registry bool) ([]Migration, error) {
	migrations, err := m.source.Snapshot(ctx)
	if err != nil {
		return nil, err
	}
	known := map[Version]bool{}
	for _, g := range migrations {
		known[g.Version] = true
		for i, s := range []Script{g.Up, g.Down} {
			if err = m.driver.ValidateScript(s); err != nil {
				direction := Up
				if i == 1 {
					direction = Down
				}
				return nil, &MigrationError{Version: g.Version, Name: g.Name, Direction: direction, Stage: "validation", Err: err}
			}
		}
		if !registry {
			continue
		}
		h := m.hooks[g.Version]
		for _, d := range []Direction{Up, Down} {
			script := g.Up
			rev := h.UpRevision
			callbacks := h.BeforeUp != nil || h.AfterUp != nil
			if d == Down {
				script = g.Down
				rev = h.DownRevision
				callbacks = h.BeforeDown != nil || h.AfterDown != nil
			}
			if script.HookRevision != rev || callbacks != (rev != "") {
				return nil, &MigrationError{Version: g.Version, Name: g.Name, Direction: d, Stage: "hooks", Err: ErrHookMismatch}
			}
		}
	}
	if registry {
		for v := range m.hooks {
			if !known[v] {
				return nil, &MigrationError{Version: v, Stage: "hooks", Err: ErrHookMismatch}
			}
		}
	}
	return migrations, nil
}

func index(migrations []Migration) map[Version]Migration {
	out := map[Version]Migration{}
	for _, g := range migrations {
		out[g.Version] = g
	}
	return out
}

func validateHistory(records []Record) error {
	versions := map[Version]bool{}
	orders := map[int64]bool{}
	for _, r := range records {
		if r.Version <= 0 || r.ApplyOrder <= 0 || r.Name == "" || versions[r.Version] || orders[r.ApplyOrder] || r.DownKind != DownSQL && r.DownKind != DownNoop && r.DownKind != DownIrreversible {
			return ErrMetadataConflict
		}
		versions[r.Version] = true
		orders[r.ApplyOrder] = true
	}
	return nil
}

func (m *Migrator[T]) compare(g Migration, r Record) []Issue {
	var issues []Issue
	add := func(kind string, d Direction) { issues = append(issues, Issue{r.Version, kind, d}) }
	if g.Name != r.Name {
		add("name_mismatch", Up)
	}
	if g.Up.HookRevision != r.UpHookRevision {
		add("hook_revision_mismatch", Up)
	}
	if g.Down.HookRevision != r.DownHookRevision {
		add("hook_revision_mismatch", Down)
	}
	if g.Down.Kind != r.DownKind {
		add("down_kind_mismatch", Down)
	}
	if m.checksum != ChecksumDisabled {
		if g.Up.Checksum != r.UpChecksum {
			add("checksum_mismatch", Up)
		}
		if g.Down.Checksum != r.DownChecksum {
			add("checksum_mismatch", Down)
		}
	}
	return issues
}

func (m *Migrator[T]) check(issues []Issue, name string, d Direction, repair bool) error {
	if repair {
		for _, i := range issues {
			if i.Kind == "name_mismatch" {
				return ErrDefinitionMismatch
			}
		}
		return nil
	}
	for _, i := range issues {
		if i.Direction == Down && d == Up {
			continue
		}
		switch i.Kind {
		case "checksum_mismatch":
			if m.checksum == ChecksumStrict {
				return &MigrationError{Version: i.Version, Name: name, Direction: i.Direction, Stage: "history_check", Err: ErrChecksumMismatch}
			}
		case "name_mismatch", "down_kind_mismatch":
			return &MigrationError{Version: i.Version, Name: name, Direction: i.Direction, Stage: "history_check", Err: ErrDefinitionMismatch}
		case "hook_revision_mismatch":
			return &MigrationError{Version: i.Version, Name: name, Direction: i.Direction, Stage: "history_check", Err: ErrHookMismatch}
		}
	}
	return nil
}

func (m *Migrator[T]) Up(ctx context.Context, steps int) (Result, error) {
	if steps < 0 {
		return Result{}, ErrInvalidSteps
	}
	return m.run(ctx, Up, steps)
}

func (m *Migrator[T]) Down(ctx context.Context, steps int) (Result, error) {
	if steps <= 0 {
		return Result{}, ErrInvalidSteps
	}
	return m.run(ctx, Down, steps)
}
func (m *Migrator[T]) DownAll(ctx context.Context) (Result, error) { return m.run(ctx, Down, 0) }
func (m *Migrator[T]) run(ctx context.Context, d Direction, steps int) (result Result, err error) {
	result.Direction = d
	started := time.Now()
	m.logger.Debugw("migration run started", "operation", d, "checksum_policy", m.checksum)
	defer func() {
		if value := recover(); value != nil {
			m.logger.Errorw("migration run panicked", "operation", d, "error_kind", "panic")
			panic(value)
		}
		fields := []any{"operation", d, "duration", time.Since(started), "actions", len(result.Actions)}
		if err != nil {
			m.logger.Errorw("migration run failed", append(fields, "error_kind", errorKind(err))...)
		} else {
			m.logger.Infow("migration run completed", fields...)
		}
	}()
	migrations, err := m.snapshot(ctx, true)
	if err != nil {
		return result, err
	}
	byVersion := index(migrations)
	m.logger.Debugw("acquiring migration run lock", "operation", d)
	err = m.driver.WithSession(ctx, func(s Session[T]) error {
		if e := s.EnsureMetadata(ctx); e != nil {
			return e
		}
		records, e := s.ReadHistory(ctx)
		if e != nil {
			return e
		}
		if e = validateHistory(records); e != nil {
			return e
		}
		applied := map[Version]Record{}
		var max Version
		for _, r := range records {
			applied[r.Version] = r
			if r.Version > max {
				max = r.Version
			}
		}
		var plan []Migration
		if d == Up {
			for _, r := range records {
				g, ok := byVersion[r.Version]
				if !ok {
					result.Issues = append(result.Issues, Issue{Version: r.Version, Kind: "unknown_applied"})
					if m.unknown == UnknownAppliedError {
						return &MigrationError{Version: r.Version, Name: r.Name, Direction: Up, Stage: "planning", Err: ErrUnknownApplied}
					}
					continue
				}
				issues := m.compare(g, r)
				result.Issues = append(result.Issues, issues...)
				if e = m.check(issues, g.Name, Up, false); e != nil {
					return e
				}
			}
			for _, g := range migrations {
				if _, ok := applied[g.Version]; !ok {
					plan = append(plan, g)
				}
			}
			if steps > 0 && len(plan) > steps {
				plan = plan[:steps]
			}
		} else {
			slices.SortFunc(records, func(a, b Record) int { return cmp.Compare(b.ApplyOrder, a.ApplyOrder) })
			if steps > 0 && len(records) > steps {
				records = records[:steps]
			}
			for _, r := range records {
				g, ok := byVersion[r.Version]
				if !ok {
					return &MigrationError{Version: r.Version, Name: r.Name, Direction: Down, Stage: "planning", Err: ErrUnknownApplied}
				}
				issues := m.compare(g, r)
				result.Issues = append(result.Issues, issues...)
				if e = m.check(issues, g.Name, Down, false); e != nil {
					return e
				}
				if g.Down.Kind == DownIrreversible {
					return &MigrationError{Version: r.Version, Name: r.Name, Direction: Down, Stage: "planning", Err: ErrIrreversible}
				}
				plan = append(plan, g)
			}
		}
		m.logger.Debugw("migration plan prepared", "operation", d, "actions", len(plan))
		for _, i := range result.Issues {
			m.logger.Warnw("migration issue", "version", i.Version, "issue", i.Kind, "direction", i.Direction)
		}
		for _, g := range plan {
			if e = ctx.Err(); e != nil {
				return e
			}
			script := g.Up
			h := m.hooks[g.Version]
			before, after := h.BeforeUp, h.AfterUp
			if d == Down {
				script = g.Down
				before, after = h.BeforeDown, h.AfterDown
			}
			m.logger.Debugw("migration step started", "version", g.Version, "direction", d)
			stage := "begin"
			outcome, e := s.InTx(ctx, script, func(tx Transaction[T]) error {
				stage = "history_read"
				latest, e := tx.ReadHistory(ctx)
				if e != nil {
					return e
				}
				found := false
				for _, r := range latest {
					if r.Version == g.Version {
						found = true
						if d == Down && r != applied[g.Version] {
							return ErrHistoryConflict
						}
					}
				}
				if found != (d == Down) {
					return ErrHistoryConflict
				}
				if before != nil {
					stage = "before_hook"
					if e = before(ctx, tx.Executor()); e != nil {
						return e
					}
				}
				if script.Kind == DownSQL {
					stage = "sql"
					if e = tx.ExecScript(ctx, script); e != nil {
						return e
					}
				}
				if after != nil {
					stage = "after_hook"
					if e = after(ctx, tx.Executor()); e != nil {
						return e
					}
				}
				stage = "history_write"
				if d == Down {
					e = tx.Delete(ctx, g.Version)
				} else {
					e = tx.Insert(ctx, Record{Version: g.Version, Name: g.Name, UpChecksum: g.Up.Checksum, DownChecksum: g.Down.Checksum, DownKind: g.Down.Kind, UpHookRevision: g.Up.HookRevision, DownHookRevision: g.Down.HookRevision, AppliedAt: time.Now().UTC()})
				}
				if e == nil {
					stage = "commit"
				}
				return e
			})
			if outcome == TxCommitted {
				action := Action{g.Version, g.Name, d, d == Up && g.Version < max}
				result.Actions = append(result.Actions, action)
				m.logger.Infow("migration committed", "version", g.Version, "direction", d, "out_of_order", action.OutOfOrder)
			}
			if outcome == TxUnknown && !errors.Is(e, ErrCommitOutcomeUnknown) {
				e = errors.Join(ErrCommitOutcomeUnknown, e)
			}
			if e != nil {
				return &MigrationError{Version: g.Version, Name: g.Name, Direction: d, Stage: stage, Err: e}
			}
			if outcome != TxCommitted {
				return &MigrationError{Version: g.Version, Name: g.Name, Direction: d, Stage: stage, Err: ErrHistoryConflict}
			}
		}
		return nil
	})
	return result, err
}

func (m *Migrator[T]) Status(ctx context.Context) (Status, error) {
	var out Status
	migrations, err := m.snapshot(ctx, false)
	if err != nil {
		return out, err
	}
	records, err := m.driver.ReadHistorySnapshot(ctx)
	if err != nil {
		return out, err
	}
	if err = validateHistory(records); err != nil {
		return out, err
	}
	applied := map[Version]Record{}
	var max Version
	for _, r := range records {
		applied[r.Version] = r
		if r.Version > max {
			max = r.Version
		}
	}
	for _, g := range migrations {
		state := MigrationStatus{Version: g.Version, Name: g.Name, State: "pending", OutOfOrder: g.Version < max, DownKind: g.Down.Kind, ChecksumState: "not_applied"}
		if r, ok := applied[g.Version]; ok {
			state.State = "applied"
			state.OutOfOrder = false
			state.ApplyOrder = r.ApplyOrder
			state.ChecksumState = "match"
			if m.checksum == ChecksumDisabled {
				state.ChecksumState = "not_checked"
			}
			issues := m.compare(g, r)
			out.Issues = append(out.Issues, issues...)
			for _, i := range issues {
				if i.Kind == "checksum_mismatch" {
					state.ChecksumState = "mismatch"
				}
			}
			delete(applied, g.Version)
		}
		for _, issue := range m.hookIssues(g) {
			out.Issues = append(out.Issues, issue)
		}
		out.Migrations = append(out.Migrations, state)
	}
	for _, r := range applied {
		out.Migrations = append(out.Migrations, MigrationStatus{Version: r.Version, Name: r.Name, State: "unknown_applied", ApplyOrder: r.ApplyOrder, DownKind: r.DownKind, ChecksumState: "not_checked"})
		out.Issues = append(out.Issues, Issue{Version: r.Version, Kind: "unknown_applied"})
	}
	slices.SortFunc(out.Migrations, func(a, b MigrationStatus) int { return cmp.Compare(a.Version, b.Version) })
	slices.SortFunc(out.Issues, func(a, b Issue) int {
		if a.Version != b.Version {
			return cmp.Compare(a.Version, b.Version)
		}
		if a.Direction != b.Direction {
			return cmp.Compare(a.Direction, b.Direction)
		}
		return cmp.Compare(a.Kind, b.Kind)
	})
	return out, nil
}

func (m *Migrator[T]) Drop(ctx context.Context) error {
	err := m.driver.WithSession(ctx, func(s Session[T]) error {
		if err := s.EnsureMetadata(ctx); err != nil {
			return err
		}
		return s.Drop(ctx)
	})
	if err == nil {
		m.logger.Infow("database scope dropped", "operation", "drop")
	}
	return err
}

func (m *Migrator[T]) repairPlan(version Version, migrations []Migration, records []Record) (RepairPlan, error) {
	p := RepairPlan{Version: version}
	if err := validateHistory(records); err != nil {
		return p, err
	}
	g, ok := index(migrations)[version]
	if !ok {
		return p, ErrInvalidSource
	}
	for _, r := range records {
		if r.Version != version {
			continue
		}
		p.Before = r.DownDefinition()
		p.After = g.Down.DownDefinition()
		p.WouldChange = p.Before != p.After
		p.Issues = m.compare(g, r)
		return p, m.check(p.Issues, g.Name, Down, true)
	}
	return p, ErrNotApplied
}

func (m *Migrator[T]) PlanRepairDown(ctx context.Context, v Version) (RepairPlan, error) {
	if v <= 0 {
		return RepairPlan{}, ErrInvalidConfig
	}
	g, err := m.snapshot(ctx, false)
	if err != nil {
		return RepairPlan{}, err
	}
	r, err := m.driver.ReadHistorySnapshot(ctx)
	if err != nil {
		return RepairPlan{}, err
	}
	return m.repairPlan(v, g, r)
}

func (m *Migrator[T]) RepairDown(ctx context.Context, v Version) (out RepairResult, err error) {
	out.Version = v
	if v <= 0 {
		return out, ErrInvalidConfig
	}
	err = m.driver.WithSession(ctx, func(s Session[T]) error {
		g, e := m.snapshot(ctx, false)
		if e != nil {
			return e
		}
		r, e := s.ReadExistingHistory(ctx)
		if e != nil {
			return e
		}
		p, e := m.repairPlan(v, g, r)
		out.Before = p.Before
		out.After = p.After
		out.Issues = p.Issues
		if e != nil {
			return e
		}
		if !p.WouldChange {
			out.Confirmed = true
			return nil
		}
		outcome, e := s.InTx(ctx, Script{Kind: DownNoop}, func(tx Transaction[T]) error {
			latest, e := tx.ReadHistory(ctx)
			if e != nil {
				return e
			}
			fresh, e := m.repairPlan(v, g, latest)
			if e != nil {
				return e
			}
			if fresh.Before != p.Before {
				return ErrHistoryConflict
			}
			return tx.UpdateDownMetadata(ctx, v, p.After)
		})
		if outcome == TxCommitted {
			out.Confirmed = true
			out.Changed = true
		}
		if outcome == TxUnknown && !errors.Is(e, ErrCommitOutcomeUnknown) {
			e = errors.Join(ErrCommitOutcomeUnknown, e)
		}
		if e == nil && outcome != TxCommitted {
			return ErrHistoryConflict
		}
		return e
	})
	if out.Confirmed {
		m.logger.Infow("down metadata accepted", "operation", "repair-down", "version", v, "changed", out.Changed, "before", out.Before, "after", out.After)
	}
	return out, err
}

func (m *Migrator[T]) hookIssues(g Migration) []Issue {
	h := m.hooks[g.Version]
	var issues []Issue
	for _, entry := range []struct {
		direction            Direction
		revision, registered string
		callbacks            bool
	}{
		{Up, g.Up.HookRevision, h.UpRevision, h.BeforeUp != nil || h.AfterUp != nil},
		{Down, g.Down.HookRevision, h.DownRevision, h.BeforeDown != nil || h.AfterDown != nil},
	} {
		if entry.revision != entry.registered || entry.callbacks != (entry.registered != "") {
			kind := "hook_registration_mismatch"
			if entry.revision != "" && !entry.callbacks {
				kind = "missing_hooks"
			}
			issues = append(issues, Issue{Version: g.Version, Kind: kind, Direction: entry.direction})
		}
	}
	return issues
}
