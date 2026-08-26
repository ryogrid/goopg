package sqlparser

import "testing"

// TestUnqualifiedDMLParity — UPDATE / DELETE without a WHERE clause.
//
// upd_where and del_where had NO empty alternative, so `UPDATE t SET x = 1`
// and `DELETE FROM t` — everyday SQL — were syntax errors on the routed path,
// as was anything following the missing clause (RETURNING). The corpus never
// caught it because every harvested UPDATE/DELETE literal happens to carry a
// WHERE.
//
// update_core's ONLY alternative was also missing its leading UPDATE keyword
// (`| ONLY qualified_name ...`), so it could never reduce.
func TestUnqualifiedDMLParity(t *testing.T) {
	for _, q := range []string{
		"UPDATE foo SET x = 2",
		"UPDATE foo SET x = 2, y = 3",
		"DELETE FROM foo",
		"UPDATE foo SET x = 2 RETURNING x",
		"DELETE FROM foo RETURNING x",
		"UPDATE ONLY foo SET x = 2",
		"UPDATE ONLY foo SET x = 2 WHERE y = 1",
		"DELETE FROM ONLY foo",
		"UPDATE foo f SET x = 2",
		"DELETE FROM foo f",
		// the WHERE forms must stay identical
		"UPDATE foo SET x = 2 WHERE y = 1",
		"DELETE FROM foo WHERE y = 1",
		"UPDATE foo SET x = 2 FROM bar WHERE foo.id = bar.id",
		"DELETE FROM foo USING bar WHERE foo.id = bar.id",
	} {
		assertParity(t, q)
	}
}
