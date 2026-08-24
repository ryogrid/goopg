package executor

import (
	"testing"

	"github.com/goopg/goopg/internal/optimizer"
	"github.com/goopg/goopg/internal/parser"
)

// async_notify_test.go — pg_notify()/pg_notification_queue_usage() coverage
// for M0134-0091 (postgres/src/test/regress/sql/async.sql). Two independent
// bugs found sizing the case against the PG 18.3 oracle:
//
//  1. pg_notification_queue_usage()'s FuncCall arm was missing from
//     exprType (internal/optimizer/planner.go), so the planner advertised
//     "unknown" instead of float8 over the wire. psql right-justifies a
//     numeric column but left-justifies an unknown/text one, so
//     `SELECT pg_notification_queue_usage();` rendered "0" left-padded by a
//     single space instead of right-justified to the header's width — a
//     silent column-alignment divergence with no error, easy to miss without
//     a live oracle diff.
//  2. pg_notify(channel, payload) never validated the channel name at all.
//     PG's Async_Notify (postgres/src/backend/commands/async.c:604-621)
//     rejects an empty (or NULL, which PG's pg_notify(PG_FUNCTION_ARGS)
//     substitutes with "") channel with ERRCODE_INVALID_PARAMETER_VALUE
//     "channel name cannot be empty", and one whose length reaches
//     NAMEDATALEN (64) with "channel name too long" — neither ereport calls
//     errposition(), so PG attaches no LINE/^ cursor to either error.
func TestPgNotificationQueueUsageIsFloat8(t *testing.T) {
	ctx, _, cleanup := newDDLFixture(t)
	defer cleanup()

	// Assert the planner's advertised column type directly (Schema, not a
	// round-trip through pg_typeof's OID-rendering wire path): psql picks
	// left- vs right-justification off this type, so "unknown" vs "float8"
	// is the entire bug even though the runtime value is identical either way.
	stmts, err := parser.Parse(`SELECT pg_notification_queue_usage()`)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	plan, err := optimizer.Plan(stmts[0], ctx.Catalog)
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	out := plan.Output()
	if len(out) != 1 {
		t.Fatalf("want 1 output column, got %d", len(out))
	}
	if got := out[0].Type.Name; got != "float8" {
		t.Errorf("pg_notification_queue_usage() column type = %q, want %q", got, "float8")
	}
}

func TestPgNotifyChannelNameValidation(t *testing.T) {
	ctx, _, cleanup := newDDLFixture(t)
	defer cleanup()

	runErr := func(sql string) *ExecError {
		t.Helper()
		ctx.CommandCounterIncrement()
		ctx.CmdID = ctx.GetCurrentCommandId(true)
		stmts, perr := parser.Parse(sql)
		if perr != nil {
			t.Fatalf("Parse(%q): %v", sql, perr)
		}
		plan, err := optimizer.Plan(stmts[0], ctx.Catalog)
		if err != nil {
			t.Fatalf("Plan(%q): %v", sql, err)
		}
		op, err := Build(plan)
		if err != nil {
			t.Fatalf("Build(%q): %v", sql, err)
		}
		asExecErr := func(err error) *ExecError {
			ee, ok := err.(*ExecError)
			if !ok {
				t.Fatalf("%s: error %v is not *ExecError (%T)", sql, err, err)
			}
			return ee
		}
		if err := op.Open(ctx); err != nil {
			return asExecErr(err)
		}
		defer op.Close()
		for {
			_, err := op.Next()
			if err == EOF {
				return nil
			}
			if err != nil {
				return asExecErr(err)
			}
		}
	}

	cases := []struct {
		sql  string
		want string
	}{
		{`SELECT pg_notify('','sample message1')`, "channel name cannot be empty"},
		{`SELECT pg_notify(NULL,'sample message1')`, "channel name cannot be empty"},
		{`SELECT pg_notify('notify_async_channel_name_too_long______________________________','sample_message1')`, "channel name too long"},
	}
	for _, c := range cases {
		ee := runErr(c.sql)
		if ee == nil {
			t.Errorf("%s: want error %q, got none", c.sql, c.want)
			continue
		}
		if ee.Code != "22023" {
			t.Errorf("%s: Code = %q, want 22023", c.sql, ee.Code)
		}
		if ee.Message != c.want {
			t.Errorf("%s: Message = %q, want %q", c.sql, ee.Message, c.want)
		}
		if ee.Pos != 0 {
			t.Errorf("%s: Pos = %d, want 0 (async.c's ereport has no errposition call)", c.sql, ee.Pos)
		}
	}

	// A valid channel name should not error.
	if ee := runErr(`SELECT pg_notify('notify_async2','ok')`); ee != nil {
		t.Errorf("valid channel: unexpected error %v", ee)
	}
}
