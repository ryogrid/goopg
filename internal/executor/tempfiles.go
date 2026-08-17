package executor

import (
	"os"
	"sync"

	"github.com/goopg/goopg/internal/storage/file"
)

// tempFileRegistry is the per-query owner of every spill file the executor
// creates (M0127-P3.3; design leftdeep-joins/06 §3).
//
// Before this, each operator was individually responsible for unlinking its
// own files, and the responsibility was discharged unevenly: sortOp removed
// its chunks in Close, spillOp removed nothing at all, and NOBODY removed
// anything when a query died between creating a file and reaching Close — an
// Open that errors after the build spilled, a cancelled query, a failed
// probe. Those paths leaked a file per spill for the life of the cluster.
//
// The registry is the executor's analogue of PG's resource owner
// (postgres/src/backend/utils/resowner/resowner.c → FileClose at
// ResourceOwnerRelease): the operator still unlinks eagerly when it can — a
// long query that finishes one batch should not hold its file to query end —
// but ownership does not depend on that happening. Whatever is left when the
// statement ends is closed and unlinked by release().
//
// Concurrency: parallel workers get their own *Context (NewWorkerContext) but
// SHARE this registry by pointer, because the files a worker spills are the
// leader's query's files and must die with it even if the worker's own Close
// never runs. Every method is therefore mutex-guarded.
type tempFileRegistry struct {
	mu sync.Mutex
	// paths is a set rather than a slice: forget() is called on the hot
	// eager-unlink path (once per finished hash-join batch), and a linear
	// scan there would be quadratic in nbatch.
	paths map[string]struct{}
	// dir is the resolved spill directory, cached after the first
	// MkdirAll so a query with 1024 batches makes one syscall, not 1024.
	dir     string
	dirDone bool
	dirErr  error
}

func newTempFileRegistry() *tempFileRegistry {
	return &tempFileRegistry{paths: make(map[string]struct{})}
}

// dirFor resolves (and creates once) the directory spill files go into.
// dataDir == "" means "no cluster" — unit tests and the synthetic contexts
// built by FK/partition DDL — and falls back to the OS temp directory, which
// is what every caller did before P3.3.
func (r *tempFileRegistry) dirFor(dataDir string) (string, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.dirDone {
		return r.dir, r.dirErr
	}
	r.dirDone = true
	if dataDir == "" {
		r.dir = os.TempDir()
		return r.dir, nil
	}
	r.dir, r.dirErr = file.EnsureDir(dataDir)
	if r.dirErr != nil {
		// A cluster whose datadir is read-only should still be able to
		// spill somewhere rather than fail the query outright; PG would
		// error, but goopg's DataDir is empty in enough legitimate
		// contexts that degrading is the safer asymmetry. The error is
		// remembered so the fallback is decided once.
		r.dir, r.dirErr = os.TempDir(), nil
	}
	return r.dir, r.dirErr
}

func (r *tempFileRegistry) register(path string) {
	r.mu.Lock()
	r.paths[path] = struct{}{}
	r.mu.Unlock()
}

// forget drops a path the caller has already unlinked itself.
func (r *tempFileRegistry) forget(path string) {
	r.mu.Lock()
	delete(r.paths, path)
	r.mu.Unlock()
}

// release unlinks every file still registered and empties the set. It returns
// the number of files it had to remove — a non-zero count is normal (an
// erroring query), and the tests assert on it directly.
func (r *tempFileRegistry) release() int {
	r.mu.Lock()
	paths := r.paths
	r.paths = make(map[string]struct{})
	r.mu.Unlock()
	n := 0
	for p := range paths {
		if err := os.Remove(p); err == nil {
			n++
		}
	}
	return n
}

// spillDir returns the directory this query's spill files belong in, creating
// it on first use. A nil Context (unit-test operators built without one) gets
// the OS temp directory.
func (c *Context) spillDir() (string, error) {
	if c == nil || c.tempFiles == nil {
		return os.TempDir(), nil
	}
	return c.tempFiles.dirFor(c.DataDir)
}

// registerSpillFile takes ownership of path for the rest of the statement.
func (c *Context) registerSpillFile(path string) {
	if c == nil || c.tempFiles == nil {
		return
	}
	c.tempFiles.register(path)
}

// forgetSpillFile releases ownership of a path the operator just unlinked.
func (c *Context) forgetSpillFile(path string) {
	if c == nil || c.tempFiles == nil {
		return
	}
	c.tempFiles.forget(path)
}

// ReleaseSpillFiles unlinks every spill file this statement still owns. The
// server calls it at statement end on BOTH the simple and the extended path,
// on the error path as well as the success path — that is the whole point:
// operator Close is best-effort, this is not.
func (c *Context) ReleaseSpillFiles() int {
	if c == nil || c.tempFiles == nil {
		return 0
	}
	return c.tempFiles.release()
}

// removeSpillFile is the eager unlink: drop the file now and stop tracking it.
func (c *Context) removeSpillFile(path string) {
	if path == "" {
		return
	}
	_ = os.Remove(path)
	c.forgetSpillFile(path)
}
