// Package xmigrator executes ordered, transactional SQL migrations using separately installed drivers.
package xmigrator

import (
	"context"
	"time"
)

type (
	Version   int64
	Direction string
)

const (
	Up   Direction = "up"
	Down Direction = "down"
)

type DownKind string

const (
	DownSQL          DownKind = "sql"
	DownNoop         DownKind = "noop"
	DownIrreversible DownKind = "irreversible"
)

type ChecksumPolicy uint8

const (
	ChecksumStrict ChecksumPolicy = iota
	ChecksumWarn
	ChecksumDisabled
)

type UnknownAppliedPolicy uint8

const (
	UnknownAppliedAllow UnknownAppliedPolicy = iota
	UnknownAppliedError
)

// Script is a normalized snapshot. SQL is executed verbatim, never split at semicolons.
type Script struct {
	SQL          string
	Checksum     [32]byte
	Kind         DownKind
	HookRevision string
	ForeignKeys  *bool
}
type Migration struct {
	Version  Version
	Name     string
	Up, Down Script
}
type Record struct {
	Version          Version
	Name             string
	UpChecksum       [32]byte
	DownChecksum     [32]byte
	DownKind         DownKind
	UpHookRevision   string
	DownHookRevision string
	AppliedAt        time.Time
	ApplyOrder       int64
}
type DownDefinition struct {
	Checksum     [32]byte
	Kind         DownKind
	HookRevision string
}

func (r Record) DownDefinition() DownDefinition {
	return DownDefinition{r.DownChecksum, r.DownKind, r.DownHookRevision}
}

func (s Script) DownDefinition() DownDefinition {
	return DownDefinition{s.Checksum, s.Kind, s.HookRevision}
}

type Issue struct {
	Version   Version
	Kind      string
	Direction Direction
}
type Action struct {
	Version    Version
	Name       string
	Direction  Direction
	OutOfOrder bool
}
type Result struct {
	Direction Direction
	Actions   []Action
	Issues    []Issue
}
type MigrationStatus struct {
	Version       Version
	Name          string
	State         string
	ApplyOrder    int64
	OutOfOrder    bool
	DownKind      DownKind
	ChecksumState string
}
type Status struct {
	Migrations []MigrationStatus
	Issues     []Issue
}
type RepairPlan struct {
	Version       Version
	WouldChange   bool
	Before, After DownDefinition
	Issues        []Issue
}
type RepairResult struct {
	Version            Version
	Confirmed, Changed bool
	Before, After      DownDefinition
	Issues             []Issue
}
type (
	ValidationReport struct{ Migrations int }
	TxOutcome        uint8
)

const (
	TxNotCommitted TxOutcome = iota
	TxCommitted
	TxUnknown
)

// Driver owns connection lifetimes and a lock covering the complete callback.
type Driver[T any] interface {
	WithSession(context.Context, func(Session[T]) error) error
	ReadHistorySnapshot(context.Context) ([]Record, error)
	ValidateScript(Script) error
}
type Session[T any] interface {
	EnsureMetadata(context.Context) error
	ReadExistingHistory(context.Context) ([]Record, error)
	ReadHistory(context.Context) ([]Record, error)
	InTx(context.Context, Script, func(Transaction[T]) error) (TxOutcome, error)
	Drop(context.Context) error
}
type Transaction[T any] interface {
	Executor() T
	ExecScript(context.Context, Script) error
	ReadHistory(context.Context) ([]Record, error)
	Insert(context.Context, Record) error
	Delete(context.Context, Version) error
	UpdateDownMetadata(context.Context, Version, DownDefinition) error
}
type Hooks[T any] struct {
	UpRevision, DownRevision                 string
	BeforeUp, AfterUp, BeforeDown, AfterDown func(context.Context, T) error
}
