package parser

import "testing"

// TestKeywordDefsRowCount pins the transcription completeness of
// keywords_gen.go against the upstream oracle row count (ground truth:
// rows matching `^\s*PG_KEYWORD("` in postgres/src/include/parser/kwlist.h;
// established 2026-08-25). If this fails after an oracle refresh, rerun
// `go run ./cmd/gen-kwlist-go` — never hand-edit the generated file.
func TestKeywordDefsRowCount(t *testing.T) {
	if got := len(keywordDefs); got != 494 {
		t.Fatalf("keywordDefs rows = %d, want 494 (kwlist.h PG_KEYWORD count)", got)
	}
}

// TestKeywordDefsNoDuplicateTextOrToken guards the generator's uniqueness
// contract at the consumer side.
func TestKeywordDefsNoDuplicateTextOrToken(t *testing.T) {
	texts := make(map[string]int, len(keywordDefs))
	tokens := make(map[string]int, len(keywordDefs))
	for _, d := range keywordDefs {
		if prev := texts[d.Text]; prev > 0 {
			t.Errorf("duplicate keyword text %q", d.Text)
		}
		if prev := tokens[d.Token]; prev > 0 {
			t.Errorf("duplicate terminal name %q", d.Token)
		}
		texts[d.Text]++
		tokens[d.Token]++
	}
}

// TestKeywordDefsSpotChecks pins representative rows across all four
// categories and both label kinds, straight from kwlist.h:
//
//	PG_KEYWORD("as",            AS,            RESERVED_KEYWORD, AS_LABEL)
//	PG_KEYWORD("authorization", AUTHORIZATION, TYPE_FUNC_NAME_KEYWORD, BARE_LABEL)
//	PG_KEYWORD("between",       BETWEEN,       COL_NAME_KEYWORD, BARE_LABEL)
//	PG_KEYWORD("select",        SELECT,        RESERVED_KEYWORD, BARE_LABEL)
func TestKeywordDefsSpotChecks(t *testing.T) {
	byText := make(map[string]keywordDef, len(keywordDefs))
	for _, d := range keywordDefs {
		byText[d.Text] = d
	}

	cases := []struct {
		text string
		want keywordDef
	}{
		{"as", keywordDef{Text: "as", Token: "AS", Category: CatReserved, Label: LabelAs}},
		{"authorization", keywordDef{Text: "authorization", Token: "AUTHORIZATION", Category: CatTypeFuncName, Label: LabelBare}},
		{"between", keywordDef{Text: "between", Token: "BETWEEN", Category: CatColName, Label: LabelBare}},
		{"select", keywordDef{Text: "select", Token: "SELECT", Category: CatReserved, Label: LabelBare}},
	}
	for _, c := range cases {
		got, ok := byText[c.text]
		if !ok {
			t.Errorf("keyword %q missing from table", c.text)
			continue
		}
		if got != c.want {
			t.Errorf("keyword %q = %+v, want %+v", c.text, got, c.want)
		}
	}
}

// TestKeywordDefsCategoryCoverage asserts all four categories and both label
// kinds actually occur, so a generator regression cannot silently flatten
// them. (LabelNone legitimately never occurs: every kwlist.h PG 18.3 row
// carries BARE_LABEL or AS_LABEL.)
func TestKeywordDefsCategoryCoverage(t *testing.T) {
	var cats [4]bool
	var labels [3]bool
	for _, d := range keywordDefs {
		cats[d.Category] = true
		labels[d.Label] = true
	}
	for i, seen := range cats {
		if !seen {
			t.Errorf("category %d absent from table", i)
		}
	}
	if !labels[LabelBare] || !labels[LabelAs] {
		t.Errorf("label kinds incomplete: bare=%v as=%v", labels[LabelBare], labels[LabelAs])
	}
}
