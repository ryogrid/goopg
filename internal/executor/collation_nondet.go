package executor

import (
	"strings"

	"golang.org/x/text/collate"
	"golang.org/x/text/language"

	"github.com/goopg/goopg/internal/catalog"
)

// nondeterministicCollators returns, per column, an equality collator for a
// column whose collation is a nondeterministic ICU collation (CREATE
// COLLATION … (provider = icu, deterministic = false)), and nil for every
// other column. Under such a collation PG's texteq is varstr_cmp == 0 — the
// ICU collator decides, so canonically equivalent strings (U+00E4 and
// a + U+0308) are equal (varlena.c texteq → pg_strncoll). The ICU collator
// at its default (tertiary) strength is modelled by x/text/collate for the
// collation's locale. The result is nil when no column needs one.
func nondeterministicCollators(ctx *Context, cols []catalog.Column) []*collate.Collator {
	// The apply worker's catalog is a wrapper over InMemory.
	im, ok := underlyingInMemory(ctx.Catalog)
	if !ok {
		return nil
	}
	var out []*collate.Collator
	for i, col := range cols {
		if col.Collation == "" {
			continue
		}
		schema, name := "", col.Collation
		if dot := strings.LastIndexByte(name, '.'); dot >= 0 {
			schema, name = name[:dot], name[dot+1:]
		}
		uc := im.FindCollation(strings.Trim(name, `"`), strings.Trim(schema, `"`), catalog.NamespaceDBOid(ctx.CurrentDatabaseOid))
		if uc == nil || uc.Deterministic || uc.Provider != 'i' {
			continue
		}
		if out == nil {
			out = make([]*collate.Collator, len(cols))
		}
		out[i] = collate.New(language.Make(uc.Locale))
	}
	return out
}

// rowMatchesKeyCollated is rowMatchesKey with column collators: a text
// column with a nondeterministic collator matches when the collator finds
// the two strings equal. colls nil is rowMatchesKey.
func rowMatchesKeyCollated(row, keyRow Row, colls []*collate.Collator) bool {
	if colls == nil {
		return rowMatchesKey(row, keyRow)
	}
	for i, k := range keyRow {
		if i >= len(row) {
			return false
		}
		if k.IsNull() {
			continue
		}
		if i < len(colls) && colls[i] != nil && row[i].Kind == KindString && k.Kind == KindString {
			if colls[i].CompareString(row[i].StringValue(), k.StringValue()) != 0 {
				return false
			}
			continue
		}
		if !applyDatumEqual(row[i], k) {
			return false
		}
	}
	return true
}
