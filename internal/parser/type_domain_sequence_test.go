package parser

import "testing"

// TestTypeDomainSequenceDDL pins P5.5 — CREATE/DROP TYPE, CREATE/DROP DOMAIN,
// CREATE SEQUENCE and DO (56 + 22 + 18 + 17 + 16 unrouted regress fragments).
//
// Legacy parses NONE of these bodies as expressions — it walks raw tokens and
// stores their join — so the grammar does the structural work while the action
// rebuilds the stored text from the token stream, the division of labour CHECK
// bodies already use. Three consequences are pinned here:
//
//   - a composite field's type is stored as RAW TEXT, so
//     `character varying(20)` comes back as "character varying ( 20 )".
//   - `CHECK (VALUE IN (...))` is recognised specially: Expr stays empty and
//     the literals land in InValues.
//   - a base type's option list reaches the AST only as HasOptions; every
//     option is parsed and discarded.
func TestTypeDomainSequenceDDL(t *testing.T) {
	for _, q := range []string{

		"CREATE TYPE e AS ENUM ('a','b')", "CREATE TYPE s.e AS ENUM ('a')",
		"CREATE TYPE c AS (a int, b text COLLATE \"C\")",
		"CREATE TYPE c AS (a character varying(20), b numeric(10,2))",
		"CREATE TYPE t",
		"CREATE TYPE r AS RANGE (SUBTYPE = int4, MULTIRANGE_TYPE_NAME = mr, SUBTYPE_OPCLASS = o, COLLATION = \"C\")",
		"CREATE TYPE r AS RANGE (SUBTYPE = int4)",
		"CREATE TYPE t (INPUT = f, OUTPUT = g)",
		"CREATE TYPE t (INPUT = f, OUTPUT = g, LIKE = int8, DEFAULT = 'x', PREFERRED = true)",
		"CREATE TYPE c AS (ca char(10)[], js json, jsa json[], ia int[][])",
		"CREATE TYPE r AS RANGE (SUBTYPE = int4[])",
		"CREATE TYPE t (INPUT = f, OUTPUT = g, INTERNALLENGTH = variable, ALIGNMENT = double, STORAGE = extended, PASSEDBYVALUE)",
		"DROP TYPE t", "DROP TYPE IF EXISTS a, b CASCADE", "DROP TYPE s.t RESTRICT",
		"CREATE DOMAIN d AS text", "CREATE DOMAIN d int", "CREATE DOMAIN d AS varchar(20) NOT NULL",
		"CREATE DOMAIN d AS int CONSTRAINT c CHECK (VALUE > 0) CHECK (VALUE < 9)",
		"CREATE DOMAIN d AS int DEFAULT 1 NULL", "CREATE DOMAIN s.d AS int",
		"CREATE DOMAIN d AS int CHECK (VALUE IN (1,2))",
		"CREATE DOMAIN d AS text CHECK (VALUE IN ('a','b'))",
		"CREATE DOMAIN d AS int CHECK (VALUE > 0 AND VALUE < 10)",
		"CREATE DOMAIN d AS int[]",
		"DROP DOMAIN d", "DROP DOMAIN IF EXISTS a, b RESTRICT",
		"CREATE SEQUENCE s", "CREATE TEMP SEQUENCE s", "CREATE SEQUENCE IF NOT EXISTS s",
		"CREATE SEQUENCE s AS smallint INCREMENT BY 2 MINVALUE 1 MAXVALUE 9 START WITH 3 CACHE 4 CYCLE OWNED BY t.c",
		"CREATE SEQUENCE s NO MINVALUE NO MAXVALUE NO CYCLE OWNED BY NONE",
		"CREATE UNLOGGED SEQUENCE s", "CREATE SEQUENCE s INCREMENT -1 START -5",
		"DO $$ BEGIN END $$",
		} {
		assertParity(t, q)
	}
	// Legacy rejects an empty ENUM list, and DO takes no LANGUAGE clause on
	// either side — gram.y allows both, so the narrowing has to be pinned.
	assertBothReject(t, "CREATE TYPE e AS ENUM ()")
	assertBothReject(t, "DO LANGUAGE plpgsql $$ BEGIN END $$")
	assertBothReject(t, "DO $$ x $$ LANGUAGE plpgsql")
}
