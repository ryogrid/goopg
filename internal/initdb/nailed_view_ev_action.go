// ev_action blob lookup for the on-disk system views (M0131-S9.0).
//
// S7.4 generated the views' pg_class/pg_attribute tables from
// internal/initdb/nailed_view_manifest.tsv but left three artefacts hand-edited,
// and ledgered them as S9.1's precondition: the pg_rewrite seed rows, the
// per-view //go:embed line for each ev_action blob, and the view-OID constants.
// The embed lines are closed here.
//
// A per-view `//go:embed pg_x_ev_action.dat` + `var pgXEvAction string` pair is
// one hand-edit per view, in a file the generator does not own — exactly the
// step an S9.1 capture pass forgets, and the failure is a compile error only if
// the var is referenced. A single glob into an embed.FS scales to the 80 views
// of system_views.sql with no edit at all: drop the .dat file in, re-run
// scripts/capture-ev-action.sh + cmd/gen-nailed-view-tables, done.
//
// The blobs are stored exactly as PG's pg_rewrite.ev_action holds them — one
// line, no trailing newline, first byte '(' and last byte ')'
// (docs/design/0131-0007-ev-action-capture-tooling.md, "Query 1"). Nothing here
// trims or rewrites; a byte changed on this path is a byte a hosted PG's
// stringToNode reads.
package initdb

import (
	"embed"
	"fmt"
	"io/fs"
	"sort"
	"strings"
)

// evActionFS holds every captured ev_action blob in this package's directory.
// The pattern is deliberately name-driven rather than an explicit file list:
// nailedViewEvActionFile is the only place the naming convention lives, and
// scripts/capture-ev-action.sh writes to the same one.
//
//go:embed *_ev_action.dat
var evActionFS embed.FS

// nailedViewEvActionFile maps a system view's name to its blob's file name.
// Mirrors the emitter in scripts/capture-ev-action.sh.
func nailedViewEvActionFile(view string) string {
	return view + "_ev_action.dat"
}

// nailedViewEvAction returns the captured nodeToString(Query) blob for view.
//
// It panics on a missing or malformed blob rather than returning an error: the
// only caller is bootstrap seed construction, the inputs are compiled-in files,
// and seeding pg_rewrite with an empty or truncated ev_action produces a
// cluster whose failure surfaces much later, inside a hosted PG's rewriter,
// as a stringToNode error against a catalog nobody suspects. Fail at the point
// the data is wrong.
func nailedViewEvAction(view string) string {
	name := nailedViewEvActionFile(view)
	b, err := evActionFS.ReadFile(name)
	if err != nil {
		panic(fmt.Sprintf("initdb: no ev_action blob %s for system view %s "+
			"(capture it with scripts/capture-ev-action.sh %s): %v", name, view, view, err))
	}
	s := string(b)
	// BKI_FORCE_NOT_NULL on ev_action, and PG's stringToNode requires a
	// parenthesised outer node. A trailing newline here would be a byte of
	// difference from what upstream's own pg_rewrite row holds — the capture
	// script strips psql's exactly once, so its presence means the blob was
	// hand-edited by an editor that adds one.
	if len(s) < 2 || s[0] != '(' || s[len(s)-1] != ')' {
		panic(fmt.Sprintf("initdb: ev_action blob %s is not a parenthesised node tree "+
			"(len=%d); re-capture with scripts/capture-ev-action.sh %s", name, len(s), view))
	}
	return s
}

// nailedViewEvActionBlobs lists the view names for which a blob is embedded,
// sorted. Used by the guard that pins the blob set against the manifest's rel
// rows — a .dat file with no manifest entry is a capture whose generator run
// was skipped, and a manifest entry with no .dat file is a view that will panic
// at initdb time.
func nailedViewEvActionBlobs() []string {
	var views []string
	_ = fs.WalkDir(evActionFS, ".", func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		if name, ok := strings.CutSuffix(path, "_ev_action.dat"); ok {
			views = append(views, name)
		}
		return nil
	})
	sort.Strings(views)
	return views
}
