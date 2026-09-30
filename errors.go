package xmigrator

import (
	"errors"
	"fmt"
)

var (
	ErrInvalidConfig        = errors.New("xmigrator: invalid configuration")
	ErrInvalidSource        = errors.New("xmigrator: invalid source")
	ErrInvalidSteps         = errors.New("xmigrator: invalid steps")
	ErrVersionExists        = errors.New("xmigrator: version exists")
	ErrUnknownApplied       = errors.New("xmigrator: unknown applied migration")
	ErrChecksumMismatch     = errors.New("xmigrator: checksum mismatch")
	ErrDefinitionMismatch   = errors.New("xmigrator: definition mismatch")
	ErrHookMismatch         = errors.New("xmigrator: hook mismatch")
	ErrIrreversible         = errors.New("xmigrator: irreversible migration")
	ErrMetadataConflict     = errors.New("xmigrator: metadata conflict")
	ErrMetadataVersion      = errors.New("xmigrator: unsupported metadata version")
	ErrHistoryConflict      = errors.New("xmigrator: history precondition failed")
	ErrNotApplied           = errors.New("xmigrator: migration not applied")
	ErrUnsafeSQL            = errors.New("xmigrator: unsafe SQL")
	ErrCommitOutcomeUnknown = errors.New("xmigrator: commit outcome unknown")
	ErrDropScope            = errors.New("xmigrator: invalid drop scope")
	ErrForeignKey           = errors.New("xmigrator: foreign key violation")
)

// MigrationError identifies a failed migration while preserving its original cause.
type MigrationError struct {
	Version   Version
	Name      string
	Direction Direction
	Stage     string
	Err       error
}

func (e *MigrationError) Error() string {
	return fmt.Sprintf("xmigrator: %s migration %d (%s), %s: %v", e.Direction, e.Version, e.Name, e.Stage, e.Err)
}
func (e *MigrationError) Unwrap() error { return e.Err }

// SourceError identifies the migration file that failed validation.
type SourceError struct {
	File    string
	Version Version
	Err     error
}

func (e *SourceError) Error() string { return fmt.Sprintf("%s: %v", e.File, e.Err) }
func (e *SourceError) Unwrap() error { return e.Err }
