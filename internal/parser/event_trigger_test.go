package parser

import "testing"

// TestParseCreateEventTriggerSimple pins the no-WHEN-clause CREATE EVENT
// TRIGGER form. DU-002 (M0119-0004).
func TestParseCreateEventTriggerSimple(t *testing.T) {
	stmts, err := Parse("CREATE EVENT TRIGGER et1 ON ddl_command_start EXECUTE FUNCTION et_func()")
	if err != nil {
		t.Fatal(err)
	}
	if len(stmts) != 1 {
		t.Fatalf("len=%d want 1", len(stmts))
	}
	et, ok := stmts[0].(*CreateEventTriggerStmt)
	if !ok {
		t.Fatalf("type=%T want *CreateEventTriggerStmt", stmts[0])
	}
	if et.Name != "et1" {
		t.Errorf("Name=%q", et.Name)
	}
	if et.Event != "ddl_command_start" {
		t.Errorf("Event=%q", et.Event)
	}
	if et.FilterVar != "" || len(et.Tags) != 0 {
		t.Errorf("FilterVar=%q Tags=%v want empty", et.FilterVar, et.Tags)
	}
	if et.FuncName.Name != "et_func" {
		t.Errorf("FuncName=%+v", et.FuncName)
	}
}

// TestParseCreateEventTriggerWhenTag pins the WHEN TAG IN (...) form,
// including EXECUTE PROCEDURE (the PG11+ synonym for FUNCTION).
func TestParseCreateEventTriggerWhenTag(t *testing.T) {
	stmts, err := Parse(`CREATE EVENT TRIGGER et2 ON ddl_command_end
		WHEN TAG IN ('CREATE TABLE', 'ALTER TABLE')
		EXECUTE PROCEDURE public.et_func2()`)
	if err != nil {
		t.Fatal(err)
	}
	et := stmts[0].(*CreateEventTriggerStmt)
	if et.Event != "ddl_command_end" {
		t.Errorf("Event=%q", et.Event)
	}
	if et.FilterVar != "tag" {
		t.Errorf("FilterVar=%q want tag", et.FilterVar)
	}
	if len(et.Tags) != 2 || et.Tags[0] != "CREATE TABLE" || et.Tags[1] != "ALTER TABLE" {
		t.Errorf("Tags=%v", et.Tags)
	}
	if et.FuncName.Schema != "public" || et.FuncName.Name != "et_func2" {
		t.Errorf("FuncName=%+v", et.FuncName)
	}
}

// TestParseDropEventTrigger pins `DROP EVENT TRIGGER [IF EXISTS] name`
// routing through the shared DropCompatStmt (execDropCompat's "event
// trigger" case). Before this loop "EVENT TRIGGER" mis-parsed entirely
// (TRIGGER was swallowed as the object name, see the ddl.go comment).
func TestParseDropEventTrigger(t *testing.T) {
	stmts, err := Parse("DROP EVENT TRIGGER IF EXISTS et1")
	if err != nil {
		t.Fatal(err)
	}
	dc, ok := stmts[0].(*DropCompatStmt)
	if !ok {
		t.Fatalf("type=%T want *DropCompatStmt", stmts[0])
	}
	if dc.ObjType != "event trigger" {
		t.Errorf("ObjType=%q want %q", dc.ObjType, "event trigger")
	}
	if !dc.IfExists {
		t.Errorf("IfExists=false want true")
	}
	if len(dc.Names) != 1 || dc.Names[0].Name != "et1" {
		t.Errorf("Names=%+v", dc.Names)
	}
}

// TestParseCreateEventTriggerUnrecognizedFilterVar pins that a
// non-"tag" filter variable still parses (semantic validation happens at
// exec time, mirroring PostgreSQL's CreateEventTrigger).
func TestParseCreateEventTriggerUnrecognizedFilterVar(t *testing.T) {
	stmts, err := Parse("CREATE EVENT TRIGGER et3 ON sql_drop WHEN bogus IN ('x') EXECUTE FUNCTION et_func()")
	if err != nil {
		t.Fatal(err)
	}
	et := stmts[0].(*CreateEventTriggerStmt)
	if et.FilterVar != "bogus" {
		t.Errorf("FilterVar=%q want bogus", et.FilterVar)
	}
	if len(et.Tags) != 0 {
		t.Errorf("Tags=%v want empty (only the \"tag\" filter var populates Tags)", et.Tags)
	}
}

// TestParseAlterEventTrigger pins every ALTER EVENT TRIGGER form goopg
// models: DISABLE, ENABLE [REPLICA|ALWAYS], RENAME TO, OWNER TO (including
// the CURRENT_USER/SESSION_USER/CURRENT_ROLE sentinel). DU-002 (M0119-0004,
// loop #69 ledger follow-up).
func TestParseAlterEventTrigger(t *testing.T) {
	cases := []struct {
		sql        string
		wantAction string
		wantName   string
		wantExtra  string // NewName for rename, NewOwner for owner
	}{
		{"ALTER EVENT TRIGGER et1 DISABLE", "disable", "et1", ""},
		{"ALTER EVENT TRIGGER et1 ENABLE", "enable", "et1", ""},
		{"ALTER EVENT TRIGGER et1 ENABLE REPLICA", "enable_replica", "et1", ""},
		{"ALTER EVENT TRIGGER et1 ENABLE ALWAYS", "enable_always", "et1", ""},
		{"ALTER EVENT TRIGGER et1 RENAME TO et2", "rename", "et1", "et2"},
		{"ALTER EVENT TRIGGER et1 OWNER TO alice", "owner", "et1", "alice"},
		{"ALTER EVENT TRIGGER et1 OWNER TO CURRENT_USER", "owner", "et1", "current_user"},
		{"ALTER EVENT TRIGGER et1 OWNER TO session_user", "owner", "et1", "current_user"},
	}
	for _, c := range cases {
		stmts, err := Parse(c.sql)
		if err != nil {
			t.Fatalf("%s: %v", c.sql, err)
		}
		if len(stmts) != 1 {
			t.Fatalf("%s: len=%d want 1", c.sql, len(stmts))
		}
		aet, ok := stmts[0].(*AlterEventTriggerStmt)
		if !ok {
			t.Fatalf("%s: type=%T want *AlterEventTriggerStmt", c.sql, stmts[0])
		}
		if aet.Name != c.wantName {
			t.Errorf("%s: Name=%q want %q", c.sql, aet.Name, c.wantName)
		}
		if aet.Action != c.wantAction {
			t.Errorf("%s: Action=%q want %q", c.sql, aet.Action, c.wantAction)
		}
		switch c.wantAction {
		case "rename":
			if aet.NewName != c.wantExtra {
				t.Errorf("%s: NewName=%q want %q", c.sql, aet.NewName, c.wantExtra)
			}
		case "owner":
			if aet.NewOwner != c.wantExtra {
				t.Errorf("%s: NewOwner=%q want %q", c.sql, aet.NewOwner, c.wantExtra)
			}
		}
	}
}
