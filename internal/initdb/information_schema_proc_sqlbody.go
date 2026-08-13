// prosqlbody blob lookup for the information_schema helper functions
// (M0133-S2).
//
// The 11 helper functions `information_schema.sql` creates are new-style
// SQL-standard bodies: 10 of them carry prosrc='' and a non-null
// pg_proc.prosqlbody — a pg_node_tree exactly like pg_rewrite.ev_action
// (finding F34 in docs/design/0131-0009-system-view-corpus-widening.md). The
// same conclusion the `pgnodes` non-path reaches for ev_action therefore
// applies: these bodies are CAPTURED verbatim from a throwaway PG 18.3, not
// generated. Only `_pg_expandarray` has a textual prosrc and prosqlbody=NULL,
// so it has no blob here.
//
// The blobs are stored exactly as PG's pg_proc.prosqlbody holds them — one
// line, no trailing newline. Unlike ev_action, the outer node is NOT always
// parenthesised: a single-statement `RETURN <expr>` body nodeToString's to
// `{QUERY ...}` while `_pg_index_position`'s BEGIN ATOMIC body is a
// parenthesised List. Nothing here asserts a delimiter; a byte changed on this
// path is a byte a hosted PG's stringToNode reads.
package initdb

import (
	"embed"
	"fmt"
	"io/fs"
	"sort"
	"strings"
)

// procSqlBodyFS holds every captured prosqlbody blob in this package's
// directory. The pattern is name-driven, mirroring evActionFS for the view
// corpus: nailedProcSqlBodyFile is the only place the naming convention lives,
// and scripts/capture-ev-action.sh --prosqlbody writes to the same one.
//
//go:embed *_prosqlbody.dat
var procSqlBodyFS embed.FS

// nailedProcSqlBodyFile maps a helper function's name to its blob's file name.
func nailedProcSqlBodyFile(name string) string {
	return name + "_prosqlbody.dat"
}

// nailedProcSqlBody returns the captured nodeToString blob for the
// information_schema helper function `name`.
//
// It panics on a missing or malformed blob rather than returning an error, for
// the same reason nailedViewEvAction does: the only caller is bootstrap seed
// construction, the inputs are compiled-in files, and seeding pg_proc with an
// empty or truncated prosqlbody produces a cluster whose failure surfaces much
// later, inside a hosted PG's parse analysis, as a stringToNode error against a
// catalog nobody suspects.
func nailedProcSqlBody(name string) string {
	fn := nailedProcSqlBodyFile(name)
	b, err := procSqlBodyFS.ReadFile(fn)
	if err != nil {
		panic(fmt.Sprintf("initdb: no prosqlbody blob %s for helper %s "+
			"(capture it with scripts/capture-ev-action.sh --prosqlbody): %v", fn, name, err))
	}
	s := string(b)
	// A captured blob is non-empty and single-line (the capture script asserts
	// both); a trailing newline here would be a byte of difference from what
	// upstream's own pg_proc row holds. Fail at the point the data is wrong.
	if s == "" || strings.ContainsAny(s, "\n\r") {
		panic(fmt.Sprintf("initdb: prosqlbody blob %s is empty or spans lines "+
			"(len=%d); re-capture with scripts/capture-ev-action.sh --prosqlbody", fn, len(s)))
	}
	return s
}

// nailedProcSqlBodyBlobs lists the helper names for which a blob is embedded,
// sorted. Used by the guard that pins the blob set against the manifest's proc
// rows — a .dat file with no manifest entry is a capture whose generator run
// was skipped, and a manifest entry with no .dat file is a helper that will
// panic at initdb time.
func nailedProcSqlBodyBlobs() []string {
	var names []string
	_ = fs.WalkDir(procSqlBodyFS, ".", func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		if name, ok := strings.CutSuffix(path, "_prosqlbody.dat"); ok {
			names = append(names, name)
		}
		return nil
	})
	sort.Strings(names)
	return names
}
