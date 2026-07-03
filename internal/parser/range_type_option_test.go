package parser_test

import (
	"testing"

	"github.com/goopg/goopg/internal/parser"
)

// TestParseCreateRangeTypeSubtypeOpclassAndCollation pins DU-002's
// (M0110-0001, slice 429 follow-up sub-item (a)) parsing of the
// `subtype_opclass` and `collation` `CREATE TYPE ... AS RANGE` options into
// CreateTypeStmt, matching the existing `multirange_type_name` capture
// pattern (schema qualification dropped, bare name kept).
func TestParseCreateRangeTypeSubtypeOpclassAndCollation(t *testing.T) {
	tests := []struct {
		sql         string
		wantSubtype string
		wantOpclass string
		wantColl    string
		wantMRName  string
	}{
		{
			sql:         `CREATE TYPE myrange AS RANGE (subtype = int4)`,
			wantSubtype: "int4",
		},
		{
			sql:         `CREATE TYPE myrange AS RANGE (subtype = int4, subtype_opclass = int4_ops)`,
			wantSubtype: "int4",
			wantOpclass: "int4_ops",
		},
		{
			sql:         `CREATE TYPE textrange AS RANGE (subtype = text, collation = "C")`,
			wantSubtype: "text",
			wantColl:    "C",
		},
		{
			// Schema-qualified subtype_opclass/collation: bare name kept (same
			// rationale as multirange_type_name's schema-qualification drop).
			sql:         `CREATE TYPE textrange2 AS RANGE (subtype = text, subtype_opclass = pg_catalog.text_ops, collation = pg_catalog."C")`,
			wantSubtype: "text",
			wantOpclass: "text_ops",
			wantColl:    "C",
		},
		{
			sql:         `CREATE TYPE textrange3 AS RANGE (subtype = text, collation = "C", multirange_type_name = textmultirange3)`,
			wantSubtype: "text",
			wantColl:    "C",
			wantMRName:  "textmultirange3",
		},
	}
	for _, tc := range tests {
		t.Run(tc.sql, func(t *testing.T) {
			stmts, err := parser.Parse(tc.sql)
			if err != nil {
				t.Fatalf("Parse(%q) error: %v", tc.sql, err)
			}
			ct, ok := stmts[0].(*parser.CreateTypeStmt)
			if !ok {
				t.Fatalf("expected *CreateTypeStmt, got %T", stmts[0])
			}
			if !ct.IsRange {
				t.Fatalf("IsRange = false, want true")
			}
			if ct.RangeSubtype != tc.wantSubtype {
				t.Errorf("RangeSubtype = %q, want %q", ct.RangeSubtype, tc.wantSubtype)
			}
			if ct.RangeOpclassName != tc.wantOpclass {
				t.Errorf("RangeOpclassName = %q, want %q", ct.RangeOpclassName, tc.wantOpclass)
			}
			if ct.RangeCollationName != tc.wantColl {
				t.Errorf("RangeCollationName = %q, want %q", ct.RangeCollationName, tc.wantColl)
			}
			if ct.RangeMultirangeName != tc.wantMRName {
				t.Errorf("RangeMultirangeName = %q, want %q", ct.RangeMultirangeName, tc.wantMRName)
			}
		})
	}
}

// TestParseCreateRangeTypeCanonicalSubtypeDiffStillParseAndDiscard confirms
// `canonical`/`subtype_diff` (not yet applied — sub-item (a) remains open for
// these two, since they need pre-created shell-type + function-signature
// validation support goopg doesn't have) still parse without breaking the
// statement, and don't leak into the now-applied RangeOpclassName/
// RangeCollationName fields.
func TestParseCreateRangeTypeCanonicalSubtypeDiffStillParseAndDiscard(t *testing.T) {
	const sql = `CREATE TYPE myrange AS RANGE (subtype = float8, subtype_diff = float8mi, canonical = myrange_canon)`
	stmts, err := parser.Parse(sql)
	if err != nil {
		t.Fatalf("Parse(%q) error: %v", sql, err)
	}
	ct, ok := stmts[0].(*parser.CreateTypeStmt)
	if !ok {
		t.Fatalf("expected *CreateTypeStmt, got %T", stmts[0])
	}
	if ct.RangeSubtype != "float8" {
		t.Errorf("RangeSubtype = %q, want float8", ct.RangeSubtype)
	}
	if ct.RangeOpclassName != "" || ct.RangeCollationName != "" {
		t.Errorf("RangeOpclassName/RangeCollationName should stay empty, got %q/%q", ct.RangeOpclassName, ct.RangeCollationName)
	}
}
