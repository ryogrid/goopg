package executor

import (
	"strings"
	"testing"

	"github.com/goopg/goopg/internal/catalog"
)

// TestUserTypeNameForOIDAllKinds pins userTypeNameForOID (the resolver shared
// by format_type's built-in-fallback path, the ::regtype cast, and
// RegtypeName, see expr.go) across all four user-type kinds plus their
// auto-generated array/multirange forms, in both its always-qualify and
// never-qualify predicate forms. Before this helper existed, ::regtype only ever
// consulted oidToBuiltinTypeName and rendered the bare numeric OID for any
// dynamically-allocated user-type OID (discovered live for a range-typed
// table column: `atttypid::regtype` printed "16422" instead of "myrange"
// while `format_type(atttypid, atttypmod)` correctly printed "public.myrange"
// in the same session) — DU-002 (M0110-0001) regtype/format_type
// unification follow-up. The qualify parameter (M0122-0005 pg_typeof()::oid
// follow-up) closes a later-discovered gap: userTypeNameForOID used to
// unconditionally prefix "public." regardless of whether "public" was
// actually visible on the effective search_path, diverging from real
// PostgreSQL's regtypeout/format_type. The qualify-predicate form
// (M0119-0006 slice B) renders a type's ACTUAL schema via its NamespaceOID.
func TestUserTypeNameForOIDAllKinds(t *testing.T) {
	cat := catalog.NewInMemory()

	et, err := cat.RegisterEnum("mood", []string{"sad", "ok", "happy"})
	if err != nil {
		t.Fatalf("RegisterEnum: %v", err)
	}
	dom, err := cat.RegisterDomain("zipcode", catalog.Type{Name: "text"}, false)
	if err != nil {
		t.Fatalf("RegisterDomain: %v", err)
	}
	ct := cat.RegisterCompositeTypeWithFields("addr", []catalog.CompositeField{
		{Name: "city", ColType: "text"},
	})
	rt, err := cat.RegisterRangeType("myrange", "int4", "", "", "")
	if err != nil {
		t.Fatalf("RegisterRangeType: %v", err)
	}

	cases := []struct {
		name   string
		oid    uint32
		want   string
		wantOK bool
	}{
		{"enum", et.OID, "public.mood", true},
		{"enum array", et.ArrayOID, "public.mood[]", true},
		{"domain", dom.OID, "public.zipcode", true},
		{"domain array", dom.ArrayOID, "public.zipcode[]", true},
		{"composite", ct.OID, "public.addr", true},
		{"composite array", ct.ArrayOID, "public.addr[]", true},
		{"range", rt.OID, "public.myrange", true},
		{"range array", rt.ArrayOID, "public.myrange[]", true},
		{"multirange", rt.MultirangeOID, "public.mymultirange", true},
		{"multirange array", rt.MultirangeArrayOID, "public.mymultirange[]", true},
		{"unknown oid", 999999, "", false},
	}
	for _, tc := range cases {
		got, ok := userTypeNameForOID(cat, tc.oid, func(s string) bool { return true })
		if ok != tc.wantOK || got != tc.want {
			t.Errorf("%s: userTypeNameForOID(%d, qualify=always) = (%q, %v), want (%q, %v)", tc.name, tc.oid, got, ok, tc.want, tc.wantOK)
		}
		wantUnqualified := strings.TrimPrefix(tc.want, "public.")
		got, ok = userTypeNameForOID(cat, tc.oid, func(s string) bool { return false })
		if ok != tc.wantOK || got != wantUnqualified {
			t.Errorf("%s: userTypeNameForOID(%d, qualify=never) = (%q, %v), want (%q, %v)", tc.name, tc.oid, got, ok, wantUnqualified, tc.wantOK)
		}
	}
}

// TestUserTypeOIDForNameAllKinds pins the reverse direction (name → OID),
// the generalization of the previous enum-only `'name'::regtype` lookup to
// all four user-type kinds for symmetry with userTypeNameForOID.
func TestUserTypeOIDForNameAllKinds(t *testing.T) {
	cat := catalog.NewInMemory()

	et, err := cat.RegisterEnum("mood", []string{"sad", "ok", "happy"})
	if err != nil {
		t.Fatalf("RegisterEnum: %v", err)
	}
	dom, err := cat.RegisterDomain("zipcode", catalog.Type{Name: "text"}, false)
	if err != nil {
		t.Fatalf("RegisterDomain: %v", err)
	}
	ct := cat.RegisterCompositeTypeWithFields("addr", []catalog.CompositeField{
		{Name: "city", ColType: "text"},
	})
	rt, err := cat.RegisterRangeType("myrange", "int4", "", "", "")
	if err != nil {
		t.Fatalf("RegisterRangeType: %v", err)
	}

	cases := []struct {
		name   string
		lookup string
		want   uint32
		wantOK bool
	}{
		{"enum", "mood", et.OID, true},
		{"domain", "zipcode", dom.OID, true},
		{"composite", "addr", ct.OID, true},
		{"range", "myrange", rt.OID, true},
		{"multirange", "mymultirange", rt.MultirangeOID, true},
		{"unknown", "nosuchtype", 0, false},
	}
	for _, tc := range cases {
		got, ok := userTypeOIDForName(cat, tc.lookup)
		if ok != tc.wantOK || got != tc.want {
			t.Errorf("%s: userTypeOIDForName(%q) = (%d, %v), want (%d, %v)", tc.name, tc.lookup, got, ok, tc.want, tc.wantOK)
		}
	}
}
