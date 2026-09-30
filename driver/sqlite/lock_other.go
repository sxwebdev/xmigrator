//go:build !windows && !darwin && !linux && !freebsd && !openbsd && !netbsd && !dragonfly

package sqlite

import (
	"os"

	x "github.com/sxwebdev/xmigrator"
)

func tryLock(*os.File) (bool, error) { return false, x.ErrInvalidConfig }
func unlock(*os.File) error          { return x.ErrInvalidConfig }
