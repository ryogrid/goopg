package parser

import "testing"

// TestParseListenNotifyUnlisten verifies the LISTEN/NOTIFY/UNLISTEN grammar
// added for M0118-0009 (async-notify).
func TestParseListenNotifyUnlisten(t *testing.T) {
	t.Run("LISTEN", func(t *testing.T) {
		stmt := parseOne(t, "LISTEN c1")
		ls, ok := stmt.(*ListenStmt)
		if !ok {
			t.Fatalf("got %T, want *ListenStmt", stmt)
		}
		if ls.Channel != "c1" {
			t.Fatalf("channel = %q, want c1", ls.Channel)
		}
	})

	t.Run("NOTIFY no payload", func(t *testing.T) {
		stmt := parseOne(t, "NOTIFY c1")
		ns, ok := stmt.(*NotifyStmt)
		if !ok {
			t.Fatalf("got %T, want *NotifyStmt", stmt)
		}
		if ns.Channel != "c1" || ns.HasPayload {
			t.Fatalf("got channel=%q hasPayload=%v, want c1 false", ns.Channel, ns.HasPayload)
		}
	})

	t.Run("NOTIFY with payload", func(t *testing.T) {
		stmt := parseOne(t, "NOTIFY c2, 'hello'")
		ns, ok := stmt.(*NotifyStmt)
		if !ok {
			t.Fatalf("got %T, want *NotifyStmt", stmt)
		}
		if ns.Channel != "c2" || !ns.HasPayload || ns.Payload != "hello" {
			t.Fatalf("got channel=%q hasPayload=%v payload=%q, want c2 true hello", ns.Channel, ns.HasPayload, ns.Payload)
		}
	})

	t.Run("UNLISTEN channel", func(t *testing.T) {
		stmt := parseOne(t, "UNLISTEN c1")
		us, ok := stmt.(*UnlistenStmt)
		if !ok {
			t.Fatalf("got %T, want *UnlistenStmt", stmt)
		}
		if us.Channel != "c1" || us.All {
			t.Fatalf("got channel=%q all=%v, want c1 false", us.Channel, us.All)
		}
	})

	t.Run("UNLISTEN star", func(t *testing.T) {
		stmt := parseOne(t, "UNLISTEN *")
		us, ok := stmt.(*UnlistenStmt)
		if !ok {
			t.Fatalf("got %T, want *UnlistenStmt", stmt)
		}
		if !us.All {
			t.Fatalf("UNLISTEN * should set All")
		}
	})

	t.Run("quoted channel preserves case", func(t *testing.T) {
		stmt := parseOne(t, `LISTEN "MixedCase"`)
		ls, ok := stmt.(*ListenStmt)
		if !ok {
			t.Fatalf("got %T, want *ListenStmt", stmt)
		}
		if ls.Channel != "MixedCase" {
			t.Fatalf("channel = %q, want MixedCase", ls.Channel)
		}
	})
}

func parseOne(t *testing.T, sql string) Stmt {
	t.Helper()
	stmts, err := Parse(sql)
	if err != nil {
		t.Fatalf("parse %q: %v", sql, err)
	}
	if len(stmts) != 1 {
		t.Fatalf("parse %q: got %d statements, want 1", sql, len(stmts))
	}
	return stmts[0]
}
