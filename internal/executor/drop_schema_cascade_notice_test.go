package executor

import "testing"

// TestDropSchemaCascadeNoticeSingleObjectForm pins PG 18.3's
// reportDependentObjects (dependency.c) for DROP SCHEMA ... CASCADE. Exactly
// one dependent object is reported in the message itself with no DETAIL;
// two or more use the "N other objects" message with one DETAIL line each.
//
//	DROP SCHEMA sa CASCADE  -> NOTICE:  drop cascades to table sa.t
//	DROP SCHEMA sb CASCADE  -> NOTICE:  drop cascades to 2 other objects
//	                           DETAIL:  drop cascades to table sb.t
//	                           drop cascades to table sb.u
func TestDropSchemaCascadeNoticeSingleObjectForm(t *testing.T) {
	ctx, _, cleanup := newDDLFixture(t)
	defer cleanup()

	for _, q := range []string{
		"CREATE SCHEMA sa", "CREATE TABLE sa.t (a int)",
		"CREATE SCHEMA sb", "CREATE TABLE sb.t (a int)", "CREATE TABLE sb.u (a int)",
	} {
		if err := runDDL(t, ctx, q); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
	}

	ctx.Notices, ctx.NoticesWithDetail = nil, nil
	if err := runDDL(t, ctx, "DROP SCHEMA sa CASCADE"); err != nil {
		t.Fatal(err)
	}
	if len(ctx.NoticesWithDetail) != 0 || len(ctx.Notices) != 1 || ctx.Notices[0] != "drop cascades to table sa.t" {
		t.Errorf("one dependent: notices %q, with detail %+v; want only %q",
			ctx.Notices, ctx.NoticesWithDetail, "drop cascades to table sa.t")
	}

	ctx.Notices, ctx.NoticesWithDetail = nil, nil
	if err := runDDL(t, ctx, "DROP SCHEMA sb CASCADE"); err != nil {
		t.Fatal(err)
	}
	want := NoticeWithDetail{Message: "drop cascades to 2 other objects",
		Detail: "drop cascades to table sb.t\ndrop cascades to table sb.u"}
	if len(ctx.Notices) != 0 || len(ctx.NoticesWithDetail) != 1 || ctx.NoticesWithDetail[0] != want {
		t.Errorf("two dependents: notices %q, with detail %+v; want %+v", ctx.Notices, ctx.NoticesWithDetail, want)
	}
}
