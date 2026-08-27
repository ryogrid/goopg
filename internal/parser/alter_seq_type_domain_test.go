package parser

import "testing"

// TestAlterSequenceTypeDomain pins P5.10 — ALTER SEQUENCE / TYPE / DOMAIN.
//
// ALTER SEQUENCE differs from CREATE SEQUENCE in a way that is easy to miss:
// the NO forms are RECORDED here (NoMinValue / NoMaxValue / NoCycle) because a
// sequence already has values, so "reset to the type default" is a different
// statement from "leave unchanged" — whereas CREATE's option loop consumes the
// word and records nothing. `OWNED BY NONE` likewise sets ClearOwnedBy rather
// than storing an empty owner.
//
// ALTER TYPE's attribute subcommands are a COMMA-SEPARATED list whose first
// entry is mirrored into the legacy scalar fields (the executor reads those
// when there is at most one), and the attribute type is stored as a RAW token
// join: `numeric(3,1)` comes back as "numeric ( 3 , 1 )".
//
// ALTER DOMAIN ADD CONSTRAINT shares CREATE DOMAIN's check reader, so the
// `VALUE IN (...)` special case lands in CheckInValues there too.
func TestAlterSequenceTypeDomain(t *testing.T) {
	for _, q := range []string{

		"ALTER SEQUENCE s RESTART", "ALTER SEQUENCE s RESTART WITH 5",
		"ALTER SEQUENCE IF EXISTS s INCREMENT BY 2 NO MINVALUE NO MAXVALUE NO CYCLE",
		"ALTER SEQUENCE s AS bigint START WITH 3 CACHE 4 CYCLE OWNED BY t.c",
		"ALTER SEQUENCE s OWNED BY NONE", "ALTER SEQUENCE s SET LOGGED", "ALTER SEQUENCE s SET UNLOGGED",
		"ALTER SEQUENCE s MINVALUE 1 MAXVALUE 9",
		"ALTER TYPE t ADD VALUE 'x'", "ALTER TYPE t ADD VALUE IF NOT EXISTS 'x' BEFORE 'y'",
		"ALTER TYPE t ADD VALUE 'x' AFTER 'y'",
		"ALTER TYPE t RENAME VALUE 'a' TO 'b'", "ALTER TYPE t RENAME TO u",
		"ALTER TYPE t OWNER TO r", "ALTER TYPE t OWNER TO CURRENT_USER",
		"ALTER TYPE t ADD ATTRIBUTE a numeric(3,1)", "ALTER TYPE s.t ADD VALUE 'x'",
		"ALTER DOMAIN d SET NOT NULL", "ALTER DOMAIN d DROP NOT NULL",
		"ALTER DOMAIN d SET DEFAULT 1", "ALTER DOMAIN d DROP DEFAULT",
		"ALTER DOMAIN d ADD CONSTRAINT c CHECK (VALUE > 0)",
		"ALTER DOMAIN d ADD CHECK (VALUE IN (1,2))",
		"ALTER DOMAIN d DROP CONSTRAINT c", "ALTER DOMAIN d DROP CONSTRAINT IF EXISTS c",
		"ALTER DOMAIN d RENAME CONSTRAINT c TO d2", "ALTER DOMAIN d RENAME TO e",
		"ALTER DOMAIN d OWNER TO r",
			"ALTER TYPE t DROP ATTRIBUTE a", "ALTER TYPE t DROP ATTRIBUTE IF EXISTS a CASCADE",
		"ALTER TYPE t ALTER ATTRIBUTE a TYPE text", "ALTER TYPE t ALTER ATTRIBUTE a SET DATA TYPE text CASCADE",
		"ALTER TYPE t ADD ATTRIBUTE a int, DROP ATTRIBUTE b",
		"ALTER TYPE t ADD ATTRIBUTE a int COLLATE \"C\"",
	} {
		assertParity(t, q)
	}
	// VALIDATE CONSTRAINT is the one ALTER DOMAIN action legacy answers with a
	// CompatNoopStmt built by a skip-to-semicolon scan, so it must stay on the
	// legacy path rather than become a 42601.
	assertNotRouted(t, "ALTER DOMAIN d VALIDATE CONSTRAINT c")
}
