package parser

import "testing"

// TestInsertOnConflictReturning — like TestCreateTableV0 this asserted nothing
// (t.Logf + fmt.Printf through the legacy parser only), which is how the
// dropped OnConflictTarget.Exprs slice stayed invisible. See
// TestOnConflictArbiterParity for that defect's dedicated pin.
func TestInsertOnConflictReturning(t *testing.T) {
	for _, q := range []string{
		"INSERT INTO t VALUES (1) ON CONFLICT DO NOTHING",
		"INSERT INTO t VALUES (1) ON CONFLICT (id) DO NOTHING",
		"INSERT INTO t VALUES (1) ON CONFLICT ON CONSTRAINT c DO NOTHING",
		"INSERT INTO t VALUES (1) ON CONFLICT (id) DO UPDATE SET v = 2",
		"INSERT INTO t VALUES (1) ON CONFLICT (id) DO UPDATE SET v = 2 WHERE t.w > 0",
		"INSERT INTO t VALUES (1) RETURNING *",
		"INSERT INTO t VALUES (1) RETURNING id, v + 1 AS nv",
	} {
		assertParity(t, q)
	}
}
