package executor

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/goopg/goopg/internal/utils/mmgr"
	"github.com/goopg/goopg/internal/storage/file"
)

// countTempFiles returns the pgsql_tmp entries under a datadir's temp dir.
func countTempFiles(t *testing.T, dataDir string) []string {
	t.Helper()
	entries, err := os.ReadDir(file.Dir(dataDir))
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		t.Fatalf("read temp dir: %v", err)
	}
	var names []string
	for _, e := range entries {
		names = append(names, e.Name())
	}
	return names
}

// A spill file must land inside the CLUSTER, under PG's directory, with PG's
// prefix. Before M0127-P3.3 it went to os.TempDir() as "goopg-spill-*.tmp",
// which put query state outside the datadir and gave the startup sweep nothing
// recognisable to reclaim.
func TestSpillFileUsesPGDirectoryAndPrefix(t *testing.T) {
	ctx := NewContext()
	ctx.DataDir = t.TempDir()

	w, err := newSpillWriter(ctx)
	if err != nil {
		t.Fatalf("newSpillWriter: %v", err)
	}
	defer ctx.ReleaseSpillFiles()
	_ = w.Close()

	if got, want := filepath.Dir(w.Path()), file.Dir(ctx.DataDir); got != want {
		t.Errorf("spill file directory = %q, want %q", got, want)
	}
	if base := filepath.Base(w.Path()); !strings.HasPrefix(base, file.FilePrefix) {
		t.Errorf("spill file %q lacks the %q prefix the crash sweep filters on", base, file.FilePrefix)
	}
}

// The registry's reason to exist: a query that dies between creating a spill
// file and reaching the operator's Close must not leak it. This is the shape
// of an Open that errors after the build spilled, and of a cancelled query.
func TestReleaseSpillFilesUnlinksAbandonedFiles(t *testing.T) {
	ctx := NewContext()
	ctx.DataDir = t.TempDir()

	for i := 0; i < 3; i++ {
		w, err := newSpillWriter(ctx)
		if err != nil {
			t.Fatalf("newSpillWriter %d: %v", i, err)
		}
		if err := w.WriteRow(Row{NewIntDatum(int64(i))}); err != nil {
			t.Fatalf("WriteRow: %v", err)
		}
		// Deliberately no Close and no unlink: the operator "died".
	}
	if got := countTempFiles(t, ctx.DataDir); len(got) != 3 {
		t.Fatalf("expected 3 spill files before release, got %v", got)
	}

	if n := ctx.ReleaseSpillFiles(); n != 3 {
		t.Errorf("ReleaseSpillFiles removed %d files, want 3", n)
	}
	if got := countTempFiles(t, ctx.DataDir); len(got) != 0 {
		t.Errorf("statement end left strays behind: %v", got)
	}
	// Idempotent: the server's defer can fire after an operator already
	// released, and a second release must not double-count or panic.
	if n := ctx.ReleaseSpillFiles(); n != 0 {
		t.Errorf("second ReleaseSpillFiles removed %d files, want 0", n)
	}
}

// spillOp owns the file drainRowsBounded hands it. Its Close used to close the
// reader and leave the file on disk (design leftdeep-joins/06 §3, the named
// leak); it must now unlink and deregister so a long statement does not
// accumulate one file per bounded drain.
func TestSpillOpCloseUnlinksItsFile(t *testing.T) {
	ctx := NewContext()
	ctx.DataDir = t.TempDir()

	rows := make([]Row, 2000)
	for i := range rows {
		rows[i] = Row{NewStringDatum(strings.Repeat("x", 512))}
	}
	op, err := drainRowsBounded(ctx, &rowsOp{rows: rows}, 1024)
	if err != nil {
		t.Fatalf("drainRowsBounded: %v", err)
	}
	sp, ok := op.(*spillOp)
	if !ok {
		t.Fatalf("expected a spillOp, got %T", op)
	}
	path := sp.r.path
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("spill file missing while the operator is live: %v", err)
	}
	if err := sp.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Errorf("spillOp.Close left %s on disk", path)
	}
	// Deregistered too — otherwise release() would try (and fail) to
	// remove a path that no longer exists and mis-report its count.
	if n := ctx.ReleaseSpillFiles(); n != 0 {
		t.Errorf("ReleaseSpillFiles removed %d files after Close, want 0", n)
	}
}

// A parallel worker's spill files belong to the LEADER's statement. The worker
// Context dies when its goroutine returns — a per-worker registry would lose
// exactly the files whose operator Close never ran (a cancelled fan-out).
func TestWorkerContextSharesTheLeaderRegistry(t *testing.T) {
	leader := NewContext()
	leader.DataDir = t.TempDir()

	arena := mmgr.Acquire(nil, mmgr.KindStmt)
	defer arena.Release()
	wctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	worker := NewWorkerContext(leader, arena, wctx)

	w, err := newSpillWriter(worker)
	if err != nil {
		t.Fatalf("newSpillWriter on worker ctx: %v", err)
	}
	_ = w.Close()
	if dir := filepath.Dir(w.Path()); dir != file.Dir(leader.DataDir) {
		t.Errorf("worker spilled to %q, want the leader's %q", dir, file.Dir(leader.DataDir))
	}
	if n := leader.ReleaseSpillFiles(); n != 1 {
		t.Fatalf("leader released %d worker files, want 1", n)
	}
	if got := countTempFiles(t, leader.DataDir); len(got) != 0 {
		t.Errorf("worker spill file survived the leader's release: %v", got)
	}
}

// Injected crash: a backend is SIGKILLed mid-query, so nothing runs — no
// operator Close, no statement-end release. The files survive, which is the
// point; the startup sweep is what must reclaim them, and it must find them
// because they carry PG's prefix in PG's directory.
func TestStartupSweepReclaimsCrashedQueryFiles(t *testing.T) {
	dataDir := t.TempDir()

	func() {
		ctx := NewContext()
		ctx.DataDir = dataDir
		for i := 0; i < 4; i++ {
			w, err := newSpillWriter(ctx)
			if err != nil {
				t.Fatalf("newSpillWriter %d: %v", i, err)
			}
			_ = w.WriteRow(Row{NewIntDatum(int64(i))})
			_ = w.Close()
		}
		// The process "dies" here: ctx goes out of scope with four files
		// still registered and no release.
	}()

	if got := countTempFiles(t, dataDir); len(got) != 4 {
		t.Fatalf("expected 4 files to survive the crash, got %v", got)
	}
	n, err := file.RemoveStrayFiles(dataDir)
	if err != nil {
		t.Fatalf("RemoveStrayFiles: %v", err)
	}
	if n != 4 {
		t.Errorf("startup sweep removed %d files, want 4", n)
	}
	if got := countTempFiles(t, dataDir); len(got) != 0 {
		t.Errorf("restart left strays behind: %v", got)
	}
}

// A Context built as a bare literal (unit-test operators, the synthetic
// contexts the FK and partition-DDL paths copy) has no registry. Every
// registry call must degrade to a no-op rather than panic, and spilling must
// still work — it just falls back to the OS temp directory.
func TestNilRegistryDegradesToOSTempDir(t *testing.T) {
	ctx := &Context{}
	w, err := newSpillWriter(ctx)
	if err != nil {
		t.Fatalf("newSpillWriter with no registry: %v", err)
	}
	defer os.Remove(w.Path())
	_ = w.Close()

	if dir := filepath.Dir(w.Path()); dir != strings.TrimSuffix(os.TempDir(), string(os.PathSeparator)) {
		t.Errorf("fallback spilled to %q, want %q", dir, os.TempDir())
	}
	if n := ctx.ReleaseSpillFiles(); n != 0 {
		t.Errorf("registry-less release reported %d files", n)
	}
	ctx.forgetSpillFile(w.Path()) // must not panic
	var nilCtx *Context
	if n := nilCtx.ReleaseSpillFiles(); n != 0 {
		t.Errorf("nil-context release reported %d files", n)
	}
}
