package executor

import (
	"testing"

	"github.com/goopg/goopg/internal/catalog"
)

// TestBuiltinTypeNamesFollowFormatTypeBe pins regtypeout / format_type over
// the full pg_catalog type set (M0149-0001h1a21): jsonb, the pseudo-types
// and the range types printed as a bare OID through ::regtype and as ???
// through format_type, so psql's \dAo showed @>(3802,3802).
func TestBuiltinTypeNamesFollowFormatTypeBe(t *testing.T) {
	cat := catalog.NewInMemory()
	for oid, want := range map[uint32]string{
		3802: "jsonb", 2277: "anyarray", 2283: "anyelement", 3831: "anyrange",
		3904: "int4range", 1043: "character varying", 23: "integer", 18: `"char"`,
	} {
		if got := RegtypeName(cat, oid, func(string) bool { return false }); got != want {
			t.Errorf("regtypeout(%d) = %q, want %q", oid, got, want)
		}
		if got, ok := catalog.BuiltinFormatTypeName(oid); !ok || got != want {
			t.Errorf("format_type(%d, NULL) = %q, want %q", oid, got, want)
		}
	}
}

// TestRegtypeInputResolvesEveryBuiltin pins regtypein over the full
// pg_catalog type set (M0149-0001h1a22): 'cstring'::regtype raised 42704
// because TypeNameToOID knows only a subset, which broke opr_sanity's
// conproc signature check.
func TestRegtypeInputResolvesEveryBuiltin(t *testing.T) {
	ctx := &Context{Catalog: catalog.NewInMemory()}
	for in, want := range map[string]int64{"cstring": 2275, "anyelement": 2283, "int4range": 3904, "internal": 2281, "int4": 23} {
		d, err := regIdentifierInput(NewStringDatum(in), "regtype", ctx, 0)
		if err != nil || d.Kind != KindInt || d.Int != want {
			t.Errorf("%s::regtype = %+v, %v; want %d", in, d, err, want)
		}
	}
}
