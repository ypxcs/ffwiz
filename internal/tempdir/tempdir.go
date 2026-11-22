// Package tempdir owns the temporary directory a session writes its generated
// files into - today the concatenation list, and palettes when the GIF task
// grows one - and removes it when the session ends (plan.md §8).
//
// It is a registry rather than a bare os.MkdirTemp with a defer because a defer
// does not run when the process is sent a signal, and "cleaned up on exit,
// including after cancel or failure" is the requirement (DECISIONS D10). An
// idempotent Flush covers the ordinary exit and the failure path; Watch covers
// the signal path.
package tempdir

import (
	"context"
	"os"
	"sync"
)

// Registry is one temporary directory and the rule for removing it.
type Registry struct {
	dir  string
	once sync.Once
	err  error
}

// New creates the directory. pattern is passed to os.MkdirTemp, so "ffwiz-*"
// produces names like "ffwiz-741853621".
func New(pattern string) (*Registry, error) {
	dir, err := os.MkdirTemp("", pattern)
	if err != nil {
		return nil, err
	}
	return &Registry{dir: dir}, nil
}

// Dir is the directory generated files are written into.
func (r *Registry) Dir() string {
	if r == nil {
		return ""
	}
	return r.dir
}

// Flush removes the directory and everything in it.
//
// It is safe to call more than once and from more than one goroutine: only the
// first call does the work and the rest see its result. That is what lets the
// ordinary exit, a failed run and a signal all call it without any of them
// having to know whether another already did.
func (r *Registry) Flush() error {
	if r == nil {
		return nil
	}
	r.once.Do(func() {
		if r.dir != "" {
			r.err = os.RemoveAll(r.dir)
		}
	})
	return r.err
}

// Watch removes the directory once ctx is done, which is how a signal - SIGTERM,
// or a SIGINT that arrived while the terminal was not in raw mode - still cleans
// up. It returns immediately, and the ordinary exit path still calls Flush.
func (r *Registry) Watch(ctx context.Context) {
	if r == nil || r.dir == "" {
		return
	}
	go func() {
		<-ctx.Done()
		_ = r.Flush()
	}()
}
