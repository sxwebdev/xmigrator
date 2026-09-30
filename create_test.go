package xmigrator_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	x "github.com/sxwebdev/xmigrator"
)

func TestCreate(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	r, e := x.Create(t.Context(), x.CreateOptions{Dir: dir, Name: "create_users", Now: func() time.Time { return time.Date(2026, 9, 30, 12, 0, 1, 0, time.FixedZone("test", 3600)) }})
	if e != nil || r.Version != 20260930110001 {
		t.Fatalf("%+v %v", r, e)
	}
	for _, file := range []string{r.Up, r.Down} {
		b, e := os.ReadFile(file)
		if e != nil || string(b) != "-- TODO: implement migration\n" {
			t.Fatalf("template %s %v", b, e)
		}
	}
	s, _ := x.NewSource(os.DirFS(dir), ".")
	if _, e = x.Validate(t.Context(), s); !errors.Is(e, x.ErrInvalidSource) {
		t.Fatal("TODO passed validate", e)
	}
	if _, e = x.Create(t.Context(), x.CreateOptions{Dir: dir, Name: "other", Version: r.Version}); !errors.Is(e, x.ErrVersionExists) {
		t.Fatal(e)
	}
}

func TestCreateInvalid(t *testing.T) {
	for _, tt := range []struct {
		name string
		o    x.CreateOptions
	}{
		{"no_dir", x.CreateOptions{Name: "x"}}, {"no_name", x.CreateOptions{Dir: t.TempDir()}}, {"traversal", x.CreateOptions{Dir: t.TempDir(), Name: "../x"}}, {"negative", x.CreateOptions{Dir: t.TempDir(), Name: "x", Version: -1}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if _, e := x.Create(t.Context(), tt.o); !errors.Is(e, x.ErrInvalidConfig) {
				t.Fatal(e)
			}
		})
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, e := x.Create(ctx, x.CreateOptions{Dir: t.TempDir(), Name: "x"}); !errors.Is(e, context.Canceled) {
		t.Fatal(e)
	}
	if _, e := x.Create(t.Context(), x.CreateOptions{Dir: filepath.Join(t.TempDir(), "missing"), Name: "x"}); !errors.Is(e, os.ErrNotExist) {
		t.Fatal(e)
	}
}

func TestCreateConflictsAndConcurrency(t *testing.T) {
	dir := t.TempDir()
	if e := os.WriteFile(filepath.Join(dir, "001_other.up.sql"), []byte("SELECT 1;"), 0o600); e != nil {
		t.Fatal(e)
	}
	if _, e := x.Create(t.Context(), x.CreateOptions{Dir: dir, Name: "x", Version: 1}); !errors.Is(e, x.ErrVersionExists) {
		t.Fatal(e)
	}
	if e := os.WriteFile(filepath.Join(dir, "bad.sql"), nil, 0o600); e != nil {
		t.Fatal(e)
	}
	if _, e := x.Create(t.Context(), x.CreateOptions{Dir: dir, Name: "x", Version: 2}); !errors.Is(e, x.ErrInvalidSource) {
		t.Fatal(e)
	}
	dir = t.TempDir()
	var wg sync.WaitGroup
	errs := make(chan error, 2)
	start := make(chan struct{})
	for _, name := range []string{"users", "orders"} {
		wg.Go(func() {
			<-start
			_, e := x.Create(t.Context(), x.CreateOptions{Dir: dir, Name: name, Version: 200})
			errs <- e
		})
	}
	close(start)
	wg.Wait()
	close(errs)
	success := 0
	for e := range errs {
		if e == nil {
			success++
		} else if !errors.Is(e, x.ErrVersionExists) {
			t.Fatal(e)
		}
	}
	entries, e := os.ReadDir(dir)
	if e != nil {
		t.Fatal(e)
	}
	if success > 1 || len(entries) != 2*success {
		t.Fatalf("duplicate/orphan after completed create: success=%d files=%d", success, len(entries))
	}
}
