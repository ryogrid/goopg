package executor

import (
	"testing"

	"github.com/goopg/goopg/internal/catalog"
	"github.com/goopg/goopg/internal/utils/adt/array"
)

// TestAclItemScalarStorage pins M0149-0001k2: in a cluster with the
// fixed_length_aclitem capability an aclitem column holds PG's 16-byte
// AclItem (grantee, grantor, AclMode) and decodes through aclitemout with the
// following column intact; role names resolve through the session catalog
// (an unknown one is 42704, as get_role_oid reports); without the capability
// the value keeps goopg's older varlena text.
func TestAclItemScalarStorage(t *testing.T) {
	defer catalog.SetFixedLengthAclItem(catalog.FixedLengthAclItem())
	ctx := NewContext()
	cat := catalog.NewInMemory()
	ctx.Catalog = cat
	cols := []catalog.Column{{Name: "a", Type: catalog.Type{Name: "aclitem"}}, {Name: "after", Type: catalog.Type{Name: "int4"}}}
	regOut := func(_ string, oid uint32) string { return cat.RoleNameForOID(oid) }
	for _, on := range []bool{true, false} {
		catalog.SetFixedLengthAclItem(on)
		data, err := EncodeRowPGCtx(cols, Row{NewStringDatum("=r*w/postgres"), NewIntDatum(99)}, ctx, 0)
		if err != nil {
			t.Fatalf("capability=%v: %v", on, err)
		}
		if on && len(data) != 20 {
			t.Errorf("capable cluster: %d bytes, want 16-byte AclItem + int4", len(data))
		}
		got := make(Row, 2)
		if err := DecodeRowIntoMctxPGTupleStyled(got, cols, data, nil, 2, nil, arrayOutputStyleWith(regOut)); err != nil {
			t.Fatalf("capability=%v: decode: %v", on, err)
		}
		if got[0].StringValue() != "=r*w/postgres" || got[1].Int != 99 {
			t.Errorf("capability=%v: decoded %q, %d", on, got[0].StringValue(), got[1].Int)
		}
	}
	catalog.SetFixedLengthAclItem(true)
	if _, err := encodeAclItemScalar("nobody=r/postgres", ctx); err == nil || err.(*ExecError).Code != "42704" {
		t.Errorf("unknown role: err = %v, want 42704", err)
	}
	if s, _, _ := DecodeAclItemScalar([]byte{1, 0, 0, 0, 10, 0, 0, 0, 1, 0, 0, 0, 1, 0, 0, 0}, nil); s != "1=a*/postgres" {
		t.Errorf("dangling grantee renders %q, want the numeric OID", s)
	}
}

// arrayOutputStyleWith is the default output style with a role renderer.
func arrayOutputStyleWith(regOut func(string, uint32) string) array.OutputStyle {
	st := array.DefaultOutputStyle()
	st.RegOut = regOut
	return st
}
