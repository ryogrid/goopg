package postmaster

import "testing"

// TestIsValidSQLTypeName covers isValidSQLTypeName's array/typmod stripping
// and its extended built-in allowlist (m0134-0005-s02). Oracle: PREPARE's
// param types are PG Typename nodes (arrayBounds + typmods) resolved via a
// real pg_type lookup (postgres/src/backend/parser/parse_type.c:typenameType);
// goopg's validator is a hand-written allowlist that must mirror the same
// surface without going through the catalog.
func TestIsValidSQLTypeName(t *testing.T) {
	accepted := []string{
		"regclass[]", "regclass", "int4[][]", "text[5]",
		"varchar(10)", "numeric(10,2)", "timestamp(3) with time zone",
		"bit varying", "inet", "money", "xml", "character varying", `"char"`,
		// no-regression set
		"int4", "integer", "double precision",
		"timestamp with time zone", "bool", "unknown",
		// additional families from the brief
		"regproc", "regprocedure", "regoper", "regoperator", "regtype",
		"regrole", "regnamespace", "regconfig", "regdictionary", "regcollation",
		"bit", "varbit", "cidr", "macaddr", "macaddr8",
		"tsvector", "tsquery", "jsonpath",
		"int2vector", "oidvector", "pg_snapshot",
		"character",
	}
	for _, tc := range accepted {
		if !isValidSQLTypeName(tc) {
			t.Errorf("isValidSQLTypeName(%q) = false, want true", tc)
		}
	}

	rejected := []string{
		"nosuchtype", "nosuchtype[]", "", "   ", "int4(",
	}
	for _, tc := range rejected {
		if isValidSQLTypeName(tc) {
			t.Errorf("isValidSQLTypeName(%q) = true, want false", tc)
		}
	}
}
