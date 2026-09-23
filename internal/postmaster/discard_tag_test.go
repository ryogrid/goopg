package postmaster

import (
	"testing"

	"github.com/goopg/goopg/internal/parser"
)

// TestDiscardCommandTags: PG tags each DISCARD target separately
// (cmdtaglist.h CMDTAG_DISCARD_ALL / _PLANS / _SEQUENCES / _TEMP, chosen in
// utility.c CreateCommandTag); TEMPORARY is parsed as TEMP. goopg completed
// every form with a bare "DISCARD" and rejected DISCARD ALL outright (M-NIGHTLY
// command-tag sweep).
func TestDiscardCommandTags(t *testing.T) {
	for sql, want := range map[string]string{
		"DISCARD ALL":       "DISCARD ALL",
		"DISCARD PLANS":     "DISCARD PLANS",
		"DISCARD SEQUENCES": "DISCARD SEQUENCES",
		"DISCARD TEMP":      "DISCARD TEMP",
		"DISCARD TEMPORARY": "DISCARD TEMP",
	} {
		stmts, err := parser.Parse(sql)
		if err != nil {
			t.Fatalf("parse %q: %v", sql, err)
		}
		if got := utilityTag(stmts[0]); got != want {
			t.Errorf("%s: tag %q, want %q", sql, got, want)
		}
	}
}
