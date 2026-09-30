package xmigrator

import (
	"errors"
	"log/slog"
	"reflect"
)

type Logger interface {
	Debugw(string, ...any)
	Infow(string, ...any)
	Warnw(string, ...any)
	Errorw(string, ...any)
}
type nopLogger struct{}

func (nopLogger) Debugw(string, ...any) {}
func (nopLogger) Infow(string, ...any)  {}
func (nopLogger) Warnw(string, ...any)  {}
func (nopLogger) Errorw(string, ...any) {}

type slogLogger struct{ l *slog.Logger }

func (l slogLogger) Debugw(s string, v ...any) { l.l.Debug(s, v...) }
func (l slogLogger) Infow(s string, v ...any)  { l.l.Info(s, v...) }
func (l slogLogger) Warnw(s string, v ...any)  { l.l.Warn(s, v...) }
func (l slogLogger) Errorw(s string, v ...any) { l.l.Error(s, v...) }
func NewSlogLogger(l *slog.Logger) Logger {
	if l == nil {
		return nopLogger{}
	}
	return slogLogger{l}
}

func isNil(v any) bool {
	if v == nil {
		return true
	}
	r := reflect.ValueOf(v)
	switch r.Kind() {
	case reflect.Chan, reflect.Func, reflect.Interface, reflect.Map, reflect.Pointer, reflect.Slice:
		return r.IsNil()
	}
	return false
}

func errorKind(err error) string {
	for _, entry := range []struct {
		cause error
		kind  string
	}{
		{ErrCommitOutcomeUnknown, "commit_outcome_unknown"}, {ErrChecksumMismatch, "checksum_mismatch"}, {ErrDefinitionMismatch, "definition_mismatch"}, {ErrHookMismatch, "hook_mismatch"}, {ErrInvalidSource, "invalid_source"}, {ErrUnknownApplied, "unknown_applied"}, {ErrIrreversible, "irreversible"}, {ErrMetadataConflict, "metadata_conflict"}, {ErrMetadataVersion, "metadata_version"}, {ErrHistoryConflict, "history_conflict"}, {ErrUnsafeSQL, "unsafe_sql"},
	} {
		if errors.Is(err, entry.cause) {
			return entry.kind
		}
	}
	return "operation_failed"
}
