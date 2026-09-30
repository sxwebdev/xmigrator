package xmigrator

import (
	"context"
	"errors"
	"io"
	"io/fs"
	"os"
	"sync"
	"testing"
	"testing/fstest"
	"time"
)

var fsFault = errors.New("filesystem boundary fault")

type faultyFS struct {
	files                                                          fstest.MapFS
	reads, writes                                                  int
	failRead, failWrite                                            int
	writeError, closeError, removeError, rootCloseError, openError error
	cancel                                                         context.CancelFunc
	cancelAt                                                       string
	removed                                                        []string
}

func (f *faultyFS) ReadDir() ([]fs.DirEntry, error) {
	f.reads++
	if f.reads == f.failRead {
		return nil, fsFault
	}
	if f.cancelAt == "read" && f.reads == 2 {
		f.cancel()
	}
	return fs.ReadDir(f.files, ".")
}

func (f *faultyFS) CreateExclusive(name string) (io.WriteCloser, error) {
	f.writes++
	if f.cancelAt == "open" {
		f.cancel()
	}
	if f.writes == f.failWrite {
		return nil, f.openError
	}
	f.files[name] = &fstest.MapFile{}
	return &faultyFile{f, name}, nil
}

func (f *faultyFS) Remove(name string) error {
	f.removed = append(f.removed, name)
	if f.removeError != nil {
		return f.removeError
	}
	delete(f.files, name)
	return nil
}
func (f *faultyFS) Close() error { return f.rootCloseError }

type faultyFile struct {
	f    *faultyFS
	name string
}

func (f *faultyFile) Write(data []byte) (int, error) {
	if f.f.writeError != nil {
		return 0, f.f.writeError
	}
	f.f.files[f.name].Data = append([]byte(nil), data...)
	return len(data), nil
}

func (f *faultyFile) Close() error {
	if f.f.cancelAt == "close" && f.f.writes == 2 {
		f.f.cancel()
	}
	return f.f.closeError
}

func TestCreateBoundaryCleanup(t *testing.T) {
	for _, tt := range []struct {
		name      string
		setup     func(*faultyFS)
		want      error
		remaining int
	}{
		{"first_read", func(f *faultyFS) { f.failRead = 1 }, fsFault, 0}, {"rescan", func(f *faultyFS) { f.failRead = 2 }, fsFault, 0}, {"up_open", func(f *faultyFS) { f.failWrite = 1; f.openError = fsFault }, fsFault, 0}, {"down_open", func(f *faultyFS) { f.failWrite = 2; f.openError = fsFault }, fsFault, 0}, {"exclusive_exists", func(f *faultyFS) { f.failWrite = 1; f.openError = os.ErrExist }, ErrVersionExists, 0}, {"write", func(f *faultyFS) { f.writeError = fsFault }, fsFault, 0}, {"file_close", func(f *faultyFS) { f.closeError = fsFault }, fsFault, 0}, {"remove", func(f *faultyFS) { f.writeError = fsFault; f.removeError = fsFault }, fsFault, 1}, {"root_close", func(f *faultyFS) { f.rootCloseError = fsFault }, fsFault, 2}, {"cancel_open", func(f *faultyFS) { f.cancelAt = "open" }, context.Canceled, 0}, {"cancel_rescan", func(f *faultyFS) { f.cancelAt = "read" }, context.Canceled, 0}, {"cancel_close", func(f *faultyFS) { f.cancelAt = "close" }, context.Canceled, 0},
	} {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			ctx, cancel := context.WithCancel(t.Context())
			t.Cleanup(cancel)
			f := &faultyFS{files: fstest.MapFS{"README": {Data: []byte("keep")}}, cancel: cancel}
			tt.setup(f)
			_, e := create(ctx, CreateOptions{Dir: "migrations", Name: "users", Version: 1}, func(string) (createFS, error) { return f, nil })
			if !errors.Is(e, tt.want) {
				t.Fatal(e)
			}
			if len(f.files) != tt.remaining+1 || string(f.files["README"].Data) != "keep" {
				t.Fatalf("filesystem after error: %v", f.files)
			}
		})
	}
}

func TestCreateDateBounds(t *testing.T) {
	for _, year := range []int{-1, 0, 10000} {
		_, e := Create(t.Context(), CreateOptions{Dir: t.TempDir(), Name: "x", Now: func() time.Time { return time.Date(year, 1, 1, 0, 0, 0, 0, time.UTC) }})
		if !errors.Is(e, ErrInvalidConfig) {
			t.Fatal(e)
		}
	}
}

func TestClosedRootBoundary(t *testing.T) {
	r, e := os.OpenRoot(t.TempDir())
	if e != nil {
		t.Fatal(e)
	}
	d := rootDirectory{r}
	if e = d.Close(); e != nil {
		t.Fatal(e)
	}
	if _, e = d.ReadDir(); e == nil {
		t.Fatal("closed directory readable")
	}
	if _, e = d.CreateExclusive("x"); e == nil {
		t.Fatal("closed directory writable")
	}
	if e = d.Remove("x"); e == nil {
		t.Fatal("closed directory removable")
	}
}

type cancelAfterCheck struct {
	context.Context
	cancel context.CancelFunc
	calls  int
}

func (c *cancelAfterCheck) Err() error {
	err := c.Context.Err()
	c.calls++
	if c.calls == 2 {
		c.cancel()
	}
	return err
}

func TestCancellationBetweenScanAndCreate(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	t.Cleanup(cancel)
	c := &cancelAfterCheck{Context: ctx, cancel: cancel}
	f := &faultyFS{files: fstest.MapFS{}}
	_, e := create(c, CreateOptions{Dir: "migrations", Name: "users", Version: 1}, func(string) (createFS, error) { return f, nil })
	if !errors.Is(e, context.Canceled) || f.writes != 0 {
		t.Fatalf("writes=%d error=%v", f.writes, e)
	}
}

type scanBarrierFS struct {
	createFS
	ready   chan<- struct{}
	release <-chan struct{}
	scans   int
}

func (f *scanBarrierFS) ReadDir() ([]fs.DirEntry, error) {
	entries, e := f.createFS.ReadDir()
	f.scans++
	if f.scans == 1 {
		f.ready <- struct{}{}
		<-f.release
	}
	return entries, e
}

func TestCreateSameNameConcurrentPreflight(t *testing.T) {
	dir := t.TempDir()
	ready := make(chan struct{}, 2)
	release := make(chan struct{})
	results := make(chan error, 2)
	var wg sync.WaitGroup
	for range 2 {
		wg.Go(func() {
			_, e := create(t.Context(), CreateOptions{Dir: dir, Name: "users", Version: 42}, func(path string) (createFS, error) {
				r, e := openCreateDir(path)
				if e != nil {
					return nil, e
				}
				return &scanBarrierFS{createFS: r, ready: ready, release: release}, nil
			})
			results <- e
		})
	}
	<-ready
	<-ready
	close(release)
	wg.Wait()
	close(results)
	success := 0
	for e := range results {
		if e == nil {
			success++
		} else if !errors.Is(e, ErrVersionExists) {
			t.Fatal(e)
		}
	}
	entries, e := os.ReadDir(dir)
	if e != nil {
		t.Fatal(e)
	}
	if success != 1 || len(entries) != 2 {
		t.Fatalf("same version/name writers corrupted pair: success=%d files=%d", success, len(entries))
	}
}
