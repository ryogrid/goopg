package catalog

import "testing"

// TestAddDomainCheckNaming verifies the multi-CHECK domain model (DU-002 slice
// 385): each AddDomainCheck appends a distinct DomainCheck with its own OID, and
// unnamed checks auto-disambiguate via PG's ChooseConstraintName scheme
// (`<domain>_check`, then `<domain>_check1`, `_check2`, …), while explicit names
// are kept verbatim and do not participate in the auto-numbering.
func TestAddDomainCheckNaming(t *testing.T) {
	c := NewInMemory()
	d, err := c.RegisterDomain("multichk", Type{Name: "int4"}, false)
	if err != nil {
		t.Fatalf("RegisterDomain: %v", err)
	}

	// Two unnamed checks → multichk_check, multichk_check1.
	c.AddDomainCheck(d, "", "VALUE > 0", nil)
	c.AddDomainCheck(d, "", "VALUE < 100", nil)
	// An explicitly named check keeps its name.
	c.AddDomainCheck(d, "mix_pos", "VALUE <> 50", nil)
	// A further unnamed check skips the already-taken base and lands on _check2
	// (it must not collide with the explicit name either).
	c.AddDomainCheck(d, "", "VALUE < 200", nil)

	if len(d.Checks) != 4 {
		t.Fatalf("len(Checks) = %d, want 4", len(d.Checks))
	}

	wantNames := []string{"multichk_check", "multichk_check1", "mix_pos", "multichk_check2"}
	wantExprs := []string{"VALUE > 0", "VALUE < 100", "VALUE <> 50", "VALUE < 200"}
	seenOIDs := map[uint32]bool{}
	for i, ck := range d.Checks {
		if ck.Name != wantNames[i] {
			t.Errorf("Checks[%d].Name = %q, want %q", i, ck.Name, wantNames[i])
		}
		if ck.Expr != wantExprs[i] {
			t.Errorf("Checks[%d].Expr = %q, want %q", i, ck.Expr, wantExprs[i])
		}
		if ck.OID == 0 {
			t.Errorf("Checks[%d] (%s) has zero OID", i, ck.Name)
		}
		if seenOIDs[ck.OID] {
			t.Errorf("Checks[%d] (%s) reused OID %d", i, ck.Name, ck.OID)
		}
		seenOIDs[ck.OID] = true
	}

	// Empty expr is a no-op (mirrors the legacy SetDomainCheck guard).
	c.AddDomainCheck(d, "ignored", "", nil)
	if len(d.Checks) != 4 {
		t.Errorf("empty-expr AddDomainCheck mutated Checks: len = %d, want 4", len(d.Checks))
	}

	// InValues round-trips onto the check for the cast-enforcement path.
	c.AddDomainCheck(d, "", "VALUE = ANY (ARRAY[1, 2])", []string{"1", "2"})
	last := d.Checks[len(d.Checks)-1]
	if last.Name != "multichk_check3" {
		t.Errorf("IN-values check name = %q, want multichk_check3", last.Name)
	}
	if len(last.InValues) != 2 {
		t.Errorf("IN-values check lost InValues: %v", last.InValues)
	}
}
