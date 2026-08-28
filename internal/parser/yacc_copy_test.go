package parser

import "testing"

// TestCopyStatement pins P5.11 — COPY (gram.y CopyStmt).
//
// Three details are legacy's rather than upstream's and are easy to get wrong:
//
//   - WITH is optional before BOTH option forms. Legacy consumes it and only
//     then looks for '(' , so `WITH DELIMITER ','` is the pre-9.0 bare trail
//     with a WITH in front of it.
//   - the bare trail's vocabulary is CLOSED: parseCopyLegacyTrail returns as
//     soon as it sees a word outside binary/csv/header/freeze/delimiter/null/
//     quote/escape/encoding/force, rather than erroring.
//   - inside the parenthesised list, BOTH the option name and its value take
//     any keyword, which is how `NULL ''` and `FREEZE true` parse. The name is
//     stored raw, exactly as written.
func TestCopyStatement(t *testing.T) {
	for _, q := range []string{

		"COPY t FROM stdin", "COPY t TO stdout", "COPY s.t (a, b) FROM stdin",
		"COPY t TO '/tmp/x'", "COPY t FROM PROGRAM 'cat /tmp/x'",
		"COPY t TO PROGRAM 'cat'",
		"COPY t (a,b) TO '/tmp/x' WITH (FORMAT csv, HEADER)",
		"COPY t FROM stdin WITH (FORMAT text, DELIMITER '|', NULL '')",
		"COPY t FROM stdin (FORMAT csv)",
		"COPY t TO stdout WITH (FORCE_QUOTE *)",
		"COPY t TO stdout WITH (FORCE_QUOTE (a, b))",
		"COPY t FROM stdin WITH (FREEZE true, ENCODING 'UTF8')",
		"COPY t FROM stdin BINARY",
		"COPY t FROM stdin CSV HEADER",
		"COPY t FROM stdin DELIMITER ',' NULL '\\N'",
		"COPY t FROM stdin CSV QUOTE '\"' ESCAPE '\\\\'",
		"COPY t TO stdout CSV FORCE QUOTE *",
		"COPY t TO stdout CSV FORCE QUOTE a, b",
		"COPY t FROM stdin CSV FORCE NOT NULL a, b",
		"COPY t FROM stdin CSV FORCE NULL a",
		"COPY (SELECT 1) TO stdout",
		"COPY (SELECT * FROM t WHERE a > 1) TO '/tmp/x' WITH (FORMAT csv)",
		} {
		assertParity(t, q)
	}
	// `COPY (SELECT ... INTO t ...) TO ...` stays on the legacy path: legacy
	// stops the inner SELECT at the INTO, records SelectInto and DISCARDS
	// everything after it, so `SELECT 1 INTO frak UNION SELECT 2` keeps no
	// set-op arm at all — which a grammar cannot do to a query it has already
	// built.
	assertNotRouted(t, "COPY (SELECT 1 INTO frak UNION SELECT 2) TO 'blob'")
}
