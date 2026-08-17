package postmaster

import (
	"testing"

	"github.com/goopg/goopg/internal/catalog"
)

// TestTypeOIDForCharDisambiguation covers M0122-0005's "1-byte char (OID 18)
// disambiguation" gap for the wire-protocol RowDescription TypeOID: the
// quoted identifier "char" (pg_type OID 18, a distinct 1-byte internal type)
// must report a different TypeOID than the bare CHAR keyword (bpchar, OID
// 1042). Both share catalog.Type.Name=="char"; Args (populated from the
// CastExpr's Typmod, or from a column's declared length) is the only signal
// distinguishing them — empty Args means the quoted, typmod-less form.
func TestTypeOIDForCharDisambiguation(t *testing.T) {
	tests := []struct {
		name string
		typ  catalog.Type
		want uint32
	}{
		{"quoted char (no args)", catalog.Type{Name: "char"}, catalog.OIDChar},
		{"bare char (implicit length 1)", catalog.Type{Name: "char", Args: []int64{1}}, 1042},
		{"char with explicit length", catalog.Type{Name: "char", Args: []int64{3}}, 1042},
		{"bpchar", catalog.Type{Name: "bpchar"}, 1042},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := typeOIDFor(tc.typ); got != tc.want {
				t.Errorf("typeOIDFor(%+v) = %d, want %d", tc.typ, got, tc.want)
			}
		})
	}
}
