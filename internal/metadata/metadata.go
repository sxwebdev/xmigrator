// Package metadata defines the shared, versioned history contract.
package metadata

import (
	"regexp"

	"github.com/sxwebdev/xmigrator"
)

const (
	Owner  = "github.com/sxwebdev/xmigrator"
	Format = 1
)

var identifier = regexp.MustCompile(`^[a-z_][a-z0-9_]*$`)

func Prefix(p string) (string, error) {
	if p == "" {
		p = "__xmigrator_"
	}
	if !identifier.MatchString(p) || len(p)+7 > 63 || len(p) >= 7 && p[:7] == "sqlite_" {
		return "", xmigrator.ErrInvalidConfig
	}
	return p, nil
}
func Identifier(s string) bool { return identifier.MatchString(s) && len(s) <= 63 }
