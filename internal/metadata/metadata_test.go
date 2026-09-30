package metadata_test

import (
	"errors"
	"testing"

	x "github.com/sxwebdev/xmigrator"
	"github.com/sxwebdev/xmigrator/internal/metadata"
)

func TestPrefix(t *testing.T) {
	for _, tt := range []struct {
		prefix, want string
		err          error
	}{{"", "__xmigrator_", nil}, {"custom_", "custom_", nil}, {"UPPER", "", x.ErrInvalidConfig}, {"sqlite_", "", x.ErrInvalidConfig}, {"xxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxx", "", x.ErrInvalidConfig}, {"bad.name", "", x.ErrInvalidConfig}} {
		t.Run(tt.prefix, func(t *testing.T) {
			t.Parallel()
			p, e := metadata.Prefix(tt.prefix)
			if p != tt.want || !errors.Is(e, tt.err) {
				t.Fatalf("%q %v", p, e)
			}
		})
	}
	if !metadata.Identifier("app") || metadata.Identifier("BAD") || metadata.Identifier("xxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxx") {
		t.Fatal("identifier validation")
	}
}
