package parser

import "testing"

// TestAlterTypeSetOptions pins gram.y's `ALTER TYPE any_name SET (
// operator_def_list )`: the base-type property list, distinct from SET SCHEMA.
func TestAlterTypeSetOptions(t *testing.T) {
	stmts, err := Parse("ALTER TYPE myvarchar SET (send = myvarcharsend, receive = myvarcharrecv)")
	if err != nil {
		t.Fatal(err)
	}
	s := stmts[0].(*AlterTypeStmt)
	if s.Name != "myvarchar" || s.SetOptions["send"] != "myvarcharsend" || s.SetOptions["receive"] != "myvarcharrecv" {
		t.Fatalf("parsed %+v", s.SetOptions)
	}
}
