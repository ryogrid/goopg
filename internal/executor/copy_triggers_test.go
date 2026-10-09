package executor

import (
	"strings"
	"testing"
)

// TestCopyFromFiresTriggers pins M0146-0055 against PG 18.3. COPY FROM never
// called the trigger machinery: a BEFORE ROW trigger could not change or
// suppress a row, AFTER ROW and statement-level triggers never ran, and a
// STORED generated column was left NULL. PG's CopyFrom fires BEFORE
// STATEMENT before the first row, BEFORE ROW per row (a NULL return skips
// the row, which is not counted), queues AFTER ROW events and fires them at
// statement end, then AFTER STATEMENT — for the text and the binary format
// alike, and the statement triggers even when no row arrives. Every want is
// PG's output for the same script (NOTICE order included).
func TestCopyFromFiresTriggers(t *testing.T) {
	ctx, _, cleanup := newDDLFixture(t)
	defer cleanup()
	for _, q := range []string{
		"CREATE TABLE ct (a text, b text)",
		"CREATE TABLE ct_log (ev text)",
		`CREATE FUNCTION ct_bef() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN
			new.b := new.b || '!'; IF new.a = 'skip' THEN RETURN NULL; END IF;
			RAISE NOTICE 'before row %', new.a; RETURN new; END $$`,
		`CREATE FUNCTION ct_aft() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN
			RAISE NOTICE 'after row % %', new.a, new.b;
			INSERT INTO ct_log VALUES ('row ' || new.a); RETURN NULL; END $$`,
		`CREATE FUNCTION ct_stmt() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN
			RAISE NOTICE '% statement %', tg_when, tg_op; RETURN NULL; END $$`,
		"CREATE TRIGGER t1 BEFORE INSERT ON ct FOR EACH ROW EXECUTE FUNCTION ct_bef()",
		"CREATE TRIGGER t2 AFTER INSERT ON ct FOR EACH ROW EXECUTE FUNCTION ct_aft()",
		"CREATE TRIGGER t3 BEFORE INSERT ON ct FOR EACH STATEMENT EXECUTE FUNCTION ct_stmt()",
		"CREATE TRIGGER t4 AFTER INSERT ON ct FOR EACH STATEMENT EXECUTE FUNCTION ct_stmt()",
		"CREATE TABLE cg (a int, b int GENERATED ALWAYS AS (a * 2) STORED)",
	} {
		runSQL(t, ctx, q)
	}
	wantNotices := "BEFORE statement INSERT|before row x|before row p|after row x y!|after row p q!|AFTER statement INSERT"
	check := func(label string, rows int64, fe *CopyFromExecutor) {
		t.Helper()
		if got := strings.Join(ctx.TakeNotices(), "|"); got != wantNotices {
			t.Errorf("%s notices:\ngot  %s\nwant %s", label, got, wantNotices)
		}
		if fe.RowsInserted() != rows {
			t.Errorf("%s: COPY %d, want COPY %d (the suppressed row is not counted)", label, fe.RowsInserted(), rows)
		}
		if got := strings.Join(renderRows(runSQL(t, ctx, "SELECT a, b FROM ct ORDER BY a")), ";"); got != "p|q!;x|y!" {
			t.Errorf("%s ct = %s, want p|q!;x|y!", label, got)
		}
		if got := strings.Join(renderRows(runSQL(t, ctx, "SELECT ev FROM ct_log ORDER BY ev")), ";"); got != "row p;row x" {
			t.Errorf("%s ct_log = %s, want row p;row x", label, got)
		}
		runSQL(t, ctx, "DELETE FROM ct")
		runSQL(t, ctx, "DELETE FROM ct_log")
	}

	// Text (CSV).
	fe := newBinaryCopyExecutor(t, ctx, "COPY ct FROM STDIN WITH (FORMAT csv)")
	for _, line := range []string{"x,y", "skip,z", "p,q"} {
		if err := fe.PushLine([]byte(line)); err != nil {
			t.Fatalf("PushLine(%q): %v", line, err)
		}
	}
	if err := fe.Finish(); err != nil {
		t.Fatal(err)
	}
	check("csv", 2, fe)

	// Binary: the trailer ends the statement (no Finish on this path).
	fe = newBinaryCopyExecutor(t, ctx, "COPY ct FROM STDIN WITH (FORMAT BINARY)")
	stream := binaryCopyStream(t, fe.listedColumns(), []Row{
		{NewStringDatum("x"), NewStringDatum("y")},
		{NewStringDatum("skip"), NewStringDatum("z")},
		{NewStringDatum("p"), NewStringDatum("q")},
	})
	done, err := fe.PushBinaryData(stream)
	if err != nil || !done {
		t.Fatalf("PushBinaryData: done=%v err=%v", done, err)
	}
	check("binary", 2, fe)

	// No rows: the statement triggers still fire.
	fe = newBinaryCopyExecutor(t, ctx, "COPY ct FROM STDIN WITH (FORMAT csv)")
	if err := fe.Finish(); err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(ctx.TakeNotices(), "|"); got != "BEFORE statement INSERT|AFTER statement INSERT" {
		t.Errorf("empty COPY notices = %s, want the two statement notices", got)
	}

	// A STORED generated column is computed (PG: 21|42).
	fe = newBinaryCopyExecutor(t, ctx, "COPY cg (a) FROM STDIN")
	if err := fe.PushLine([]byte("21")); err != nil {
		t.Fatal(err)
	}
	if err := fe.Finish(); err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(renderRows(runSQL(t, ctx, "SELECT a, b FROM cg")), ";"); got != "21|42" {
		t.Errorf("cg = %s, want 21|42", got)
	}
}
