package parser

import "testing"

// TestParseCreateAccessMethod pins both AM-type forms (INDEX/TABLE) and a
// schema-qualified handler function name. DU-002 (M0119-0004).
func TestParseCreateAccessMethod(t *testing.T) {
	cases := []struct {
		sql            string
		wantName       string
		wantAMType     string
		wantHandlerSch string
		wantHandler    string
	}{
		{"CREATE ACCESS METHOD myam TYPE INDEX HANDLER myam_handler", "myam", "i", "", "myam_handler"},
		{"CREATE ACCESS METHOD mytam TYPE TABLE HANDLER public.mytam_handler", "mytam", "t", "public", "mytam_handler"},
	}
	for _, c := range cases {
		stmts, err := Parse(c.sql)
		if err != nil {
			t.Fatalf("%s: %v", c.sql, err)
		}
		if len(stmts) != 1 {
			t.Fatalf("%s: len=%d want 1", c.sql, len(stmts))
		}
		am, ok := stmts[0].(*CreateAccessMethodStmt)
		if !ok {
			t.Fatalf("%s: type=%T want *CreateAccessMethodStmt", c.sql, stmts[0])
		}
		if am.Name != c.wantName {
			t.Errorf("%s: Name=%q want %q", c.sql, am.Name, c.wantName)
		}
		if am.AMType != c.wantAMType {
			t.Errorf("%s: AMType=%q want %q", c.sql, am.AMType, c.wantAMType)
		}
		if am.HandlerName.Schema != c.wantHandlerSch || am.HandlerName.Name != c.wantHandler {
			t.Errorf("%s: HandlerName=%+v want schema=%q name=%q", c.sql, am.HandlerName, c.wantHandlerSch, c.wantHandler)
		}
	}
}

// TestParseCreateAccessMethodErrors pins the required-clause error paths.
func TestParseCreateAccessMethodErrors(t *testing.T) {
	cases := []string{
		"CREATE ACCESS METHOD myam HANDLER myam_handler",           // missing TYPE
		"CREATE ACCESS METHOD myam TYPE BOGUS HANDLER myam_handler", // bad am_type
		"CREATE ACCESS METHOD myam TYPE INDEX",                     // missing HANDLER
	}
	for _, sql := range cases {
		if _, err := Parse(sql); err == nil {
			t.Errorf("%s: want parse error, got none", sql)
		}
	}
}

// TestParseDropAccessMethod pins `DROP ACCESS METHOD [IF EXISTS] name`
// routing through the shared DropCompatStmt (execDropCompat's "access
// method" case). This form already parsed generically before this loop
// (the ident-DROP-target list); only CREATE ACCESS METHOD was previously a
// syntax error.
func TestParseDropAccessMethod(t *testing.T) {
	stmts, err := Parse("DROP ACCESS METHOD IF EXISTS myam")
	if err != nil {
		t.Fatal(err)
	}
	dc, ok := stmts[0].(*DropCompatStmt)
	if !ok {
		t.Fatalf("type=%T want *DropCompatStmt", stmts[0])
	}
	if dc.ObjType != "access method" {
		t.Errorf("ObjType=%q want %q", dc.ObjType, "access method")
	}
	if !dc.IfExists {
		t.Errorf("IfExists=false want true")
	}
	if len(dc.Names) != 1 || dc.Names[0].Name != "myam" {
		t.Errorf("Names=%+v", dc.Names)
	}
}
