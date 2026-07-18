package catalog

import "testing"

// Type OIDs used in the pins (PG18 bootstrap OIDs).
const (
	oidBool = 16
	oidInt8 = 20
	oidInt4 = 23
	oidText = 25
)

// TestLookupOperatorForNode pins the forward operator index against known
// pg_operator.dat rows (opno=OID, oprcode funcid=Code, oprresult=ResultType).
func TestLookupOperatorForNode(t *testing.T) {
	cases := []struct {
		name              string
		left, right       uint32
		wantOID, wantCode uint32
		wantResult        uint32
	}{
		{"=", oidInt4, oidInt4, 96, 65, oidBool},
		{"<>", oidInt4, oidInt4, 518, 144, oidBool},
		{"<", oidInt4, oidInt4, 97, 66, oidBool},
		{">", oidInt4, oidInt4, 521, 147, oidBool},
		{"<=", oidInt4, oidInt4, 523, 149, oidBool},
		{">=", oidInt4, oidInt4, 525, 150, oidBool},
		{"+", oidInt4, oidInt4, 551, 177, oidInt4},
		{"-", oidInt4, oidInt4, 555, 181, oidInt4},
		{"*", oidInt4, oidInt4, 514, 141, oidInt4},
		{"/", oidInt4, oidInt4, 528, 154, oidInt4},
		{"+", oidInt8, oidInt8, 684, 463, oidInt8},
		{"=", oidInt8, oidInt8, 410, 467, oidBool},
		{"=", oidText, oidText, 98, 67, oidBool},
		{"||", oidText, oidText, 654, 1258, oidText},
		{"=", oidBool, oidBool, 91, 60, oidBool},
	}
	for _, c := range cases {
		got, ok := LookupOperatorForNode(c.name, c.left, c.right)
		if !ok {
			t.Errorf("LookupOperatorForNode(%q,%d,%d) not found", c.name, c.left, c.right)
			continue
		}
		if got.OID != c.wantOID || got.Code != c.wantCode || got.ResultType != c.wantResult {
			t.Errorf("LookupOperatorForNode(%q,%d,%d) = {oid:%d code:%d result:%d}, want {oid:%d code:%d result:%d}",
				c.name, c.left, c.right, got.OID, got.Code, got.ResultType, c.wantOID, c.wantCode, c.wantResult)
		}
	}

	// Negatives: an operator that does not exist for the given operands.
	if _, ok := LookupOperatorForNode("@@@nonesuch", oidInt4, oidInt4); ok {
		t.Errorf("LookupOperatorForNode of a nonexistent operator should fail")
	}
	if _, ok := LookupOperatorForNode("||", oidInt4, oidInt4); ok {
		t.Errorf("LookupOperatorForNode(|| int4 int4) should fail (|| is text/array concat, not int4)")
	}
}

// TestLookupProcForNode pins the forward function index (name + arg OIDs →
// funcid) against known pg_proc.dat rows.
func TestLookupProcForNode(t *testing.T) {
	cases := []struct {
		name    string
		args    []uint32
		wantOID uint32
	}{
		{"now", nil, 1299},
		{"upper", []uint32{oidText}, 871},
		{"lower", []uint32{oidText}, 870},
		{"initcap", []uint32{oidText}, 872},
		{"abs", []uint32{oidInt4}, 1397},
		{"int4pl", []uint32{oidInt4, oidInt4}, 177},
	}
	for _, c := range cases {
		got, ok := LookupProcForNode(c.name, c.args)
		if !ok {
			t.Errorf("LookupProcForNode(%q,%v) not found", c.name, c.args)
			continue
		}
		if got != c.wantOID {
			t.Errorf("LookupProcForNode(%q,%v) = %d, want %d", c.name, c.args, got, c.wantOID)
		}
	}

	if _, ok := LookupProcForNode("this_function_does_not_exist", []uint32{oidInt4}); ok {
		t.Errorf("LookupProcForNode of a nonexistent function should fail")
	}
	// Wrong arg type → no such overload.
	if _, ok := LookupProcForNode("upper", []uint32{oidInt4}); ok {
		t.Errorf("LookupProcForNode(upper int4) should fail (upper takes text)")
	}
}
