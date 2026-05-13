package catalog

import (
	"fmt"
	"testing"
)

func TestEnumDomainSmoke(t *testing.T) {
	c := NewInMemory()
	
	et, err := c.RegisterEnum("rainbow", []string{"red", "orange", "yellow", "green", "blue", "purple"})
	if err != nil { t.Fatalf("RegisterEnum: %v", err) }
	t.Logf("Registered enum %q with %d values", et.Name, len(et.Values))
	
	found, ok := c.LookupEnum("rainbow")
	if !ok { t.Fatal("LookupEnum: not found") }
	t.Logf("Found enum: %v", found.Values)
	
	if err := c.AddEnumValue("rainbow", "indigo", false, "blue", ""); err != nil { t.Fatalf("AddEnumValue: %v", err) }
	found, _ = c.LookupEnum("rainbow")
	t.Logf("After add BEFORE blue: %v", found.Values)
	
	indigoIdx, blueIdx := -1, -1
	for i, v := range found.Values {
		if v == "indigo" { indigoIdx = i }
		if v == "blue" { blueIdx = i }
	}
	if indigoIdx < 0 || blueIdx < 0 || indigoIdx >= blueIdx {
		t.Errorf("indigo (%d) should be before blue (%d) in %v", indigoIdx, blueIdx, found.Values)
	}
	
	if err := c.AddEnumValue("rainbow", "red", true, "", ""); err != nil {
		t.Fatalf("AddEnumValue IF NOT EXISTS: %v", err)
	}
	if err := c.AddEnumValue("rainbow", "red", false, "", ""); err == nil {
		t.Error("expected error adding duplicate without IF NOT EXISTS")
	}
	
	d, err := c.RegisterDomain("domainint4", Type{Name: "int4"}, false)
	if err != nil { t.Fatalf("RegisterDomain: %v", err) }
	t.Logf("Domain: %s -> %s", d.Name, d.Base.Name)
	
	got := c.ResolveColumnType("domainint4")
	if got != "int4" { t.Errorf("ResolveColumnType(domainint4) = %q, want int4", got) }
	got = c.ResolveColumnType("rainbow")
	if got != "text" { t.Errorf("ResolveColumnType(rainbow) = %q, want text", got) }
	got = c.ResolveColumnType("int8")
	if got != "int8" { t.Errorf("ResolveColumnType(int8) = %q, want int8", got) }
	
	fmt.Printf("All enum/domain smoke tests pass\n")
}
