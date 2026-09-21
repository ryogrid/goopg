package executor

import "testing"

// TestCurrentDatabaseReflectsTheConnection pins that `current_database()` and
// its SQL-standard synonym `current_catalog` report the database the connection
// is actually bound to.
//
// Both used to return the literal "postgres" regardless of the connection. That
// was a wrong VALUE, not an imprecise one: a `CREATE DATABASE`d database is
// genuinely isolated in goopg — a table created in it is not visible from
// postgres — yet a client connected to it was told it was on postgres. Anything
// that routes on the answer (psql's prompt, an object browser, a migration tool
// asserting which database it is about to alter) was misinformed.
//
// The two spellings are checked TOGETHER because they are the same value by
// definition rather than by coincidence: PostgreSQL exposes one function under
// both names, so a fix that moved only one would leave a pair that disagrees.
func TestCurrentDatabaseReflectsTheConnection(t *testing.T) {
	ctx, _, cleanup := newDDLFixture(t)
	defer cleanup()

	for _, name := range []string{"postgres", "newdb", "template1"} {
		ctx.CurrentDatabase = name
		for _, sql := range []string{
			`select current_database()`,
			`select current_catalog`,
		} {
			d, _ := byteaExprResult(t, ctx, sql)
			if got := d.StringValue(); got != name {
				t.Errorf("connected to %q: %s = %q, want %q", name, sql, got, name)
			}
		}
	}

	// The embedded/test fallback. Context.CurrentDatabase's own doc records it
	// is empty in embedded contexts, so those must keep answering "postgres"
	// rather than an empty string — this arm is what makes the change safe for
	// every caller that never sets the field.
	ctx.CurrentDatabase = ""
	for _, sql := range []string{
		`select current_database()`,
		`select current_catalog`,
	} {
		d, _ := byteaExprResult(t, ctx, sql)
		if got := d.StringValue(); got != "postgres" {
			t.Errorf("unset CurrentDatabase: %s = %q, want the \"postgres\" fallback", sql, got)
		}
	}
}
