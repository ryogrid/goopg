package executor

import (
	"testing"

	"github.com/goopg/goopg/internal/catalog"
)

// TestAclitemInPG pins aclitemin / aclparse: canonical re-rendering, role
// resolution (42704), the empty-grantor and bad-mode errors (22P02), the
// PUBLIC grantee and the trailing-garbage check.
func TestAclitemInPG(t *testing.T) {
	cat := catalog.NewInMemory()
	cat.RegisterRole("alice")
	ctx := &Context{Catalog: cat}
	ok := map[string]string{
		"alice=r*/postgres":       "alice=r*/postgres",
		" alice =rw/ postgres ":   "alice=rw/postgres",
		"=X/postgres":             "=X/postgres",
		"alice=wr/postgres":       "alice=rw/postgres",
		"group alice=r/postgres":  "alice=r/postgres",
	}
	for in, want := range ok {
		got, err := aclitemIn(in, ctx)
		if err != nil || got != want {
			t.Errorf("aclitemIn(%q) = %q, %v; want %q", in, got, err, want)
		}
	}
	bad := map[string]string{
		`""=r*/""`:          `22P02 a name must follow the "/" sign`,
		"alice=r/":          `22P02 a name must follow the "/" sign`,
		"alice=r/nosuch":    `42704 role "nosuch" does not exist`,
		"alice=rY":          `22P02 invalid mode character: must be one of "arwdDxtXUCTcsAm"`,
		"alice=r/postgres x": "22P02 extra garbage at the end of the ACL specification",
		"alice= rw/postgres": "22P02 extra garbage at the end of the ACL specification",
	}
	for in, want := range bad {
		_, err := aclitemIn(in, ctx)
		if err == nil || err.Code+" "+err.Message != want {
			t.Errorf("aclitemIn(%q) error = %v; want %s", in, err, want)
		}
	}
}

// TestMakeAclItem pins makeaclitem's privilege-list parsing and rendering.
func TestMakeAclItem(t *testing.T) {
	cat := catalog.NewInMemory()
	cat.RegisterRole("alice")
	ctx := &Context{Catalog: cat}
	alice, _ := cat.RoleOID("alice")
	d, err := evalMakeAclItem([]Datum{NewIntDatum(int64(alice)), NewIntDatum(10), NewStringDatum("SELECT, INSERT,  UPDATE , DELETE  "), NewBoolDatum(false)}, ctx, 0)
	if err != nil || d.StringValue() != "alice=arwd/postgres" {
		t.Fatalf("makeaclitem = %q, %v", d.StringValue(), err)
	}
	d, _ = evalMakeAclItem([]Datum{NewIntDatum(0), NewIntDatum(10), NewStringDatum("select"), NewBoolDatum(true)}, ctx, 0)
	if d.StringValue() != "=r*/postgres" {
		t.Fatalf("PUBLIC makeaclitem = %q", d.StringValue())
	}
	if _, err := evalMakeAclItem([]Datum{NewIntDatum(10), NewIntDatum(10), NewStringDatum("SELECT, fake_privilege"), NewBoolDatum(false)}, ctx, 0); err == nil ||
		err.(*ExecError).Message != `unrecognized privilege type: "fake_privilege"` {
		t.Fatalf("fake privilege: %v", err)
	}
}
