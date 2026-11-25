package executor

import (
	"testing"
)

// TestViewDateTimeLiteralsBindAtCreate pins M0149-0001h1a81a: a view's
// date/time literals are read under the creating session's TimeZone, as PG's
// parse analysis of CREATE VIEW does, so the view answers the same in any
// session zone. goopg stored the text and read it in the querying zone.
func TestViewDateTimeLiteralsBindAtCreate(t *testing.T) {
	ctx, _, cleanup := newDDLFixture(t)
	defer cleanup()
	if err := runDDL(t, ctx, "CREATE TABLE tz (t timestamptz)"); err != nil {
		t.Fatal(err)
	}
	ctx.GetSetting = tzCtx("ISO, MDY", "UTC").GetSetting
	for _, c := range []struct{ in, want string }{
		{"SELECT t = '2020-01-01 00:00' AS m FROM tz", "SELECT t = '2020-01-01 00:00:00+00' AS m FROM tz"},
		{"SELECT t < timestamptz '2020-01-01 03:00' FROM tz", "SELECT t < timestamptz '2020-01-01 03:00:00+00' FROM tz"},
		{"SELECT 'x''y' <> 'z', t FROM tz", ""},
	} {
		got, _, ok := bindViewDateTimeLiterals(ctx, c.in, ctx.Catalog)
		if c.want == "" {
			if ok {
				t.Errorf("%s: rewrote to %q, want unchanged", c.in, got)
			}
			continue
		}
		if !ok || got != c.want {
			t.Errorf("%s:\n got %q (ok=%v)\nwant %q", c.in, got, ok, c.want)
		}
	}
	if s, e, v, ok := quotedLiteralAt("a = 'it''s' x", 4); !ok || v != "it's" || s != 4 || e != 11 {
		t.Errorf("quotedLiteralAt = %d %d %q %v", s, e, v, ok)
	}
}
