package xmigrator

import (
	"errors"
	"testing"
)

func TestErrorKinds(t *testing.T) {
	for _, err := range []error{ErrCommitOutcomeUnknown, ErrChecksumMismatch, ErrDefinitionMismatch, ErrHookMismatch, ErrInvalidSource, ErrUnknownApplied, ErrIrreversible, ErrMetadataConflict, ErrMetadataVersion, ErrHistoryConflict, ErrUnsafeSQL, errors.New("secret backend error")} {
		kind := errorKind(err)
		if kind == "" || kind == "secret backend error" {
			t.Fatal(kind)
		}
	}
}
