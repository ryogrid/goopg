package executor

import "testing"

// TestExplicitStringCastToBitPadsOrTruncates pins M0149-0001h1a83 against PG
// 18.3: an explicit cast from a string reads it with typmod -1 and then
// applies bit()/varbit() with isExplicit — bit(n) zero-pads or truncates,
// bit varying(n) truncates — where goopg raised the assignment-time length
// errors. Assignment into a bit(n) column still raises them.
func TestExplicitStringCastToBitPadsOrTruncates(t *testing.T) {
	ctx, _, cleanup := newDDLFixture(t)
	defer cleanup()
	for sql, want := range map[string]string{
		"SELECT '10'::bit(4)":         "1000",
		"SELECT bit(4) '10'":          "1000",
		"SELECT '101'::bit(2)":        "10",
		"SELECT '111'::varbit(2)":     "11",
		"SELECT '10'::bit varying(5)": "10",
		"SELECT 'x1F'::bit(4)":        "0001",
	} {
		rows := runQuery(t, ctx, sql)
		if got := rows[0][0].Format(); got != want {
			t.Errorf("%s = %s, want %s", sql, got, want)
		}
	}
	if err := runDDL(t, ctx, "CREATE TABLE bt (a bit(4))"); err != nil {
		t.Fatal(err)
	}
	if _, err := runQueryErr(t, ctx, "INSERT INTO bt VALUES ('10')"); execErrCode(err) != "22026" {
		t.Errorf("INSERT '10' into bit(4): err = %v, want 22026", err)
	}
}
