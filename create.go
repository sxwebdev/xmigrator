package xmigrator

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

type CreateOptions struct {
	Dir, Name string
	Version   Version
	Now       func() time.Time
}
type CreatedFiles struct {
	Version  Version
	Up, Down string
}

var migrationName = regexp.MustCompile(`^[a-z][a-z0-9]*(?:_[a-z0-9]+)*$`)

type createFS interface {
	ReadDir() ([]fs.DirEntry, error)
	CreateExclusive(string) (io.WriteCloser, error)
	Remove(string) error
	Close() error
}
type rootDirectory struct{ root *os.Root }

func (r rootDirectory) ReadDir() ([]fs.DirEntry, error) { return fs.ReadDir(r.root.FS(), ".") }
func (r rootDirectory) CreateExclusive(name string) (io.WriteCloser, error) {
	return r.root.OpenFile(name, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
}
func (r rootDirectory) Remove(name string) error { return r.root.Remove(name) }
func (r rootDirectory) Close() error             { return r.root.Close() }
func openCreateDir(path string) (createFS, error) {
	r, e := os.OpenRoot(path)
	if e != nil {
		return nil, e
	}
	return rootDirectory{r}, nil
}

// Create writes an exclusive up/down pair. A process crash may leave an incomplete pair;
// validation rejects it. Only files created by this call are removed on failure.
func Create(ctx context.Context, o CreateOptions) (CreatedFiles, error) {
	return create(ctx, o, openCreateDir)
}

func create(ctx context.Context, o CreateOptions, open func(string) (createFS, error)) (out CreatedFiles, err error) {
	if o.Dir == "" || !migrationName.MatchString(o.Name) || o.Version < 0 {
		return out, ErrInvalidConfig
	}
	if err = ctx.Err(); err != nil {
		return out, err
	}
	if o.Version == 0 {
		now := time.Now
		if o.Now != nil {
			now = o.Now
		}
		at := now().UTC()
		if at.Year() < 1 || at.Year() > 9999 {
			return out, ErrInvalidConfig
		}
		o.Version = Version(at.Year())*10000000000 + Version(at.Month())*100000000 + Version(at.Day())*1000000 + Version(at.Hour())*10000 + Version(at.Minute())*100 + Version(at.Second())
	}
	root, err := open(o.Dir)
	if err != nil {
		return out, err
	}
	defer func() { err = errors.Join(err, root.Close()) }()
	stem := fmt.Sprintf("%d_%s", o.Version, o.Name)
	up, down := stem+".up.sql", stem+".down.sql"
	check := func(own string) error {
		entries, e := root.ReadDir()
		if e != nil {
			return e
		}
		for _, entry := range entries {
			if entry.Name() == own || !strings.HasSuffix(entry.Name(), ".sql") {
				continue
			}
			v, _, _, e := parseFilename(entry.Name())
			if e != nil {
				return e
			}
			if v == o.Version {
				return ErrVersionExists
			}
		}
		return ctx.Err()
	}
	if err = check(""); err != nil {
		return out, err
	}
	var created []string
	defer func() {
		if err != nil {
			for _, name := range created {
				err = errors.Join(err, root.Remove(name))
			}
		}
	}()
	write := func(name string) error {
		if e := ctx.Err(); e != nil {
			return e
		}
		f, e := root.CreateExclusive(name)
		if e != nil {
			if errors.Is(e, os.ErrExist) {
				return errors.Join(ErrVersionExists, e)
			}
			return e
		}
		created = append(created, name)
		_, e = io.WriteString(f, "-- TODO: implement migration\n")
		return errors.Join(e, f.Close())
	}
	if err = write(up); err != nil {
		return out, err
	}
	if err = check(up); err != nil {
		return out, err
	}
	if err = write(down); err != nil {
		return out, err
	}
	if err = ctx.Err(); err != nil {
		return out, err
	}
	return CreatedFiles{o.Version, filepath.Join(o.Dir, up), filepath.Join(o.Dir, down)}, nil
}
