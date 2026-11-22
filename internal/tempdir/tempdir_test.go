package tempdir

import (
	"context"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

func TestNewCreatesARemovableDirectory(t *testing.T) {
	r, err := New("ffwiz-test-*")
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	dir := r.Dir()
	if info, err := os.Stat(dir); err != nil || !info.IsDir() {
		t.Fatalf("Dir() = %q, which is not a directory: %v", dir, err)
	}

	// A file written into it goes with it, which is the whole point: the list
	// ffwiz writes is not left in the user's temporary directory.
	if err := os.WriteFile(filepath.Join(dir, "list.txt"), []byte("file 'a.mp4'\n"), 0o644); err != nil {
		t.Fatalf("writing into the directory: %v", err)
	}

	if err := r.Flush(); err != nil {
		t.Fatalf("Flush: %v", err)
	}
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Errorf("the directory survived Flush: %v", err)
	}
}

// TestFlushIsIdempotent: the exit path, a failed run and a signal all flush, and
// none of them knows whether another already did (DECISIONS D10).
func TestFlushIsIdempotent(t *testing.T) {
	r, err := New("ffwiz-test-*")
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	dir := r.Dir()

	var wg sync.WaitGroup
	errs := make([]error, 4)
	for i := range errs {
		wg.Add(1)
		go func() {
			defer wg.Done()
			errs[i] = r.Flush()
		}()
	}
	wg.Wait()

	for i, err := range errs {
		if err != nil {
			t.Errorf("Flush #%d returned %v, want nil", i, err)
		}
	}
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Errorf("the directory survived concurrent Flush calls: %v", err)
	}
}

// TestWatchRemovesTheDirectoryWhenTheContextEnds covers the signal path: a
// cancelled context has to be enough, because a SIGTERM never runs a defer.
func TestWatchRemovesTheDirectoryWhenTheContextEnds(t *testing.T) {
	r, err := New("ffwiz-test-*")
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	dir := r.Dir()

	ctx, cancel := context.WithCancel(context.Background())
	r.Watch(ctx)
	cancel()

	deadline := time.Now().Add(2 * time.Second)
	for {
		if _, err := os.Stat(dir); os.IsNotExist(err) {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("the directory is still there after the context ended: %s", dir)
		}
		time.Sleep(time.Millisecond)
	}
}

// TestNilRegistryIsHarmless: a caller that never got a directory (New failed, or
// there was nothing to write) must not have to check for nil at every step.
func TestNilRegistryIsHarmless(t *testing.T) {
	var r *Registry
	if got := r.Dir(); got != "" {
		t.Errorf("Dir() = %q, want empty", got)
	}
	if err := r.Flush(); err != nil {
		t.Errorf("Flush() = %v, want nil", err)
	}
	r.Watch(context.Background())
}
