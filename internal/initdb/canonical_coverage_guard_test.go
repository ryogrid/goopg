package initdb

import (
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strings"
	"testing"
)

// TestCanonicalCoverageGuard is the perf-optimize3-dash doc 02 §7
// completeness guard: every file that consumes the canonical-WAL machinery
// (LogCanonical/PgCanonical*) or performs image-only catalog writes
// (MarkDirtyForceFPI — invisible to a LogCanonical-only grep, which is how
// the ALTER xmax-stamp path was missed by the first audit) must be listed in
// the audited allowlist below. A NEW file appearing here means a new state
// change whose native-only recovery story is unproven: extend
// analysis/perf-optimize3-dash/02-canonical-only-coverage-audit.md (does it
// have a native sibling? is it image-covered?) and then add the file here.
func TestCanonicalCoverageGuard(t *testing.T) {
	// Audited files (doc 02 §§1-7 + the S3a tests).
	allow := map[string]bool{
		"internal/catalog/canonical.go":                      true, // builders (definitions)
		"internal/executor/context.go":                       true, // LogCanonical field
		"internal/executor/operators_ddl.go":                 true, // §1 writeHeapRowCanonical + §1a stampCatalogRows/ForceFPI
		"internal/executor/operators_storage.go":             true, // §paired heap emitters + writeHeapRowReturningPG nil-hook
		"internal/executor/operators_vacuum.go":              true, // §paired VACUUM prune
		"internal/executor/operators_vacuum_datfrozenxid.go": true, // §3
		"internal/executor/sys_catalog_btree_multilevel.go":  true, // §2
		"internal/executor/sys_catalog_btree_split.go":       true, // §2
		"internal/executor/sys_catalog_index_insert.go":      true, // §2
		"internal/initdb/open.go":                            true, // choke points 1-3
		"internal/server/copy.go":                            true, // COPY wires ectx.LogCanonical (plumbing)
		"internal/server/database_ddl.go":                    true, // DDL plumbing
		"internal/server/dispatch.go":                        true, // plumbing
		"internal/server/dispatch_extended.go":               true, // plumbing
		"internal/server/server.go":                          true, // plumbing
		"internal/storage/bufpool.go":                        true, // MarkDirtyForceFPI definition
		"internal/storage/heap.go":                           true, // page-level helpers
		"internal/vacuum/vacuum.go":                          true, // §paired VACUUM prune
		"internal/wal/recovery.go":                           true, // replay side (consumer, not emitter)
	}

	_, thisFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller failed")
	}
	repoRoot := filepath.Clean(filepath.Join(filepath.Dir(thisFile), "..", ".."))
	if _, err := os.Stat(filepath.Join(repoRoot, "internal")); err != nil {
		// Guard against -trimpath builds resolving to a bogus root and the
		// walk silently finding nothing (vacuous pass).
		t.Fatalf("repo root resolution failed (%s): %v", repoRoot, err)
	}
	re := regexp.MustCompile(`LogCanonical|PgCanonical|MarkDirtyForceFPI`)

	var offenders []string
	err := filepath.Walk(filepath.Join(repoRoot, "internal"), func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		data, rerr := os.ReadFile(path)
		if rerr != nil {
			return rerr
		}
		if !re.Match(data) {
			return nil
		}
		rel, rerr := filepath.Rel(repoRoot, path)
		if rerr != nil {
			return rerr
		}
		rel = filepath.ToSlash(rel)
		if !allow[rel] {
			offenders = append(offenders, rel)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	sort.Strings(offenders)
	if len(offenders) > 0 {
		t.Fatalf("new canonical-machinery / image-only-catalog-write consumers outside the audited allowlist:\n  %s\n"+
			"Audit them in analysis/perf-optimize3-dash/02-canonical-only-coverage-audit.md, then extend the allowlist.",
			strings.Join(offenders, "\n  "))
	}
}
