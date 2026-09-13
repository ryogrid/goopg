package initdb

import (
	"testing"

	"github.com/goopg/goopg/internal/catalog"
	"github.com/goopg/goopg/internal/parser"
)

// R126: unit coverage for the two new translation helpers in the FK reload.
// They convert between pg_constraint's attnum-keyed columns and goopg's
// name-keyed catalog.ForeignKey, and a silent mistranslation here would
// repopulate ForeignKeys with the WRONG columns — which the catalog view and
// the enforcement path would both render as a healthy FK.

func TestFKAttnumsFromArrayText(t *testing.T) {
	for _, tc := range []struct {
		in   string
		want []int16
	}{
		{"{2}", []int16{2}},
		{"{2,3}", []int16{2, 3}},
		{"{1, 4}", []int16{1, 4}}, // tolerate whitespace
		// An EMPTY confkey is meaningful, not missing: it is the stored form
		// of "use the parent's PK" (catalog.go:1665), which the writer
		// preserves verbatim rather than materialising.
		{"{}", nil},
		{"", nil},
		// A value that did not decode as an array must not be half-parsed into
		// a plausible-looking attnum list.
		{"not-an-array", nil},
	} {
		got := fkAttnumsFromArrayText(tc.in)
		if len(got) != len(tc.want) {
			t.Errorf("fkAttnumsFromArrayText(%q) = %v, want %v", tc.in, got, tc.want)
			continue
		}
		for i := range got {
			if got[i] != tc.want[i] {
				t.Errorf("fkAttnumsFromArrayText(%q) = %v, want %v", tc.in, got, tc.want)
				break
			}
		}
	}
}

func TestFKColumnNames(t *testing.T) {
	tbl := &catalog.Table{Columns: []catalog.Column{
		{Name: "id"}, {Name: "pid"}, {Name: "w"},
	}}

	got, ok := fkColumnNames(tbl, []int16{2, 3})
	if !ok || len(got) != 2 || got[0] != "pid" || got[1] != "w" {
		t.Errorf("fkColumnNames({2,3}) = %v, %v; want [pid w], true", got, ok)
	}

	if got, ok := fkColumnNames(tbl, nil); !ok || len(got) != 0 {
		t.Errorf("fkColumnNames(nil) = %v, %v; want [], true", got, ok)
	}

	// An out-of-range attnum must fail the WHOLE key rather than yield a short
	// slice. keysCovering matches on names (joinrelsize.go:690), so a silently
	// truncated Columns would change which joins the FK arm fires for —
	// failing closed is the only safe direction.
	for _, bad := range [][]int16{{0}, {4}, {2, 9}} {
		if got, ok := fkColumnNames(tbl, bad); ok {
			t.Errorf("fkColumnNames(%v) = %v, true; want ok=false", bad, got)
		}
	}
}

// TestFKActionCharRoundTrip pins that the write side's FKActionChar and the
// reload's FKActionFromChar are true inverses. If they drift, an ON DELETE
// CASCADE silently becomes NO ACTION across a restart — a data-loss-shaped
// difference that nothing else in the round would catch.
func TestFKActionCharRoundTrip(t *testing.T) {
	for _, act := range []parser.FKAction{
		parser.FKActionNoAction,
		parser.FKActionRestrict,
		parser.FKActionCascade,
		parser.FKActionSetNull,
		parser.FKActionSetDefault,
	} {
		c := string(catalog.FKActionChar(act))
		if got := catalog.FKActionFromChar(c); got != act {
			t.Errorf("FKActionFromChar(FKActionChar(%v)=%q) = %v, want %v", act, c, got, act)
		}
	}
	// An unknown code degrades to NO ACTION — PG's own default, and the
	// direction that cannot invent a cascade.
	if got := catalog.FKActionFromChar("?"); got != parser.FKActionNoAction {
		t.Errorf("FKActionFromChar(%q) = %v, want NoAction", "?", got)
	}
}
