package parser

import "testing"

// TestColumnTypmodParity pins column typmods (length/precision/scale) through
// the yacc CREATE TABLE and ALTER TABLE paths against the legacy parser.
//
// WHY: grammar/goopg_ext.y's table_element action used to read cs.args from
// the CONSTRAINT carrier (colConstraints.args, a field nothing ever assigned)
// instead of the TYPE carrier (typeWithArgs.args), so every CREATE TABLE
// column typmod was silently discarded — `filler char(22)` became a plain
// `char`. The two ALTER TABLE sites read the type carrier correctly, so the
// paths had diverged; CREATE TABLE is routed, which made the loss live.
// Nothing caught it because TestCreateTableV0 asserted nothing.
func TestColumnTypmodParity(t *testing.T) {
	for _, q := range []string{
		"CREATE TABLE t (a char(22))",
		"CREATE TABLE t (a character(22))",
		"CREATE TABLE t (a varchar(10))",
		"CREATE TABLE t (a character varying(10))",
		"CREATE TABLE t (a numeric(10,2))",
		"CREATE TABLE t (a decimal(8,3))",
		"CREATE TABLE t (a bit(4))",
		"CREATE TABLE t (a timestamp(3))",
		"CREATE TABLE t (a time(3))",
		"CREATE TABLE t (a int)",
		"CREATE TABLE t (a varchar(10) not null, b numeric(10,2) default 0)",
		"ALTER TABLE t ADD COLUMN a varchar(10)",
		"ALTER TABLE t ALTER COLUMN a TYPE numeric(10,2)",
	} {
		l, n, err := diffParse(q)
		if err != nil {
			t.Errorf("%q -> %v", q, err)
			continue
		}
		if l != n {
			t.Errorf("DIFF %q\n L=%s\n N=%s", q, l, n)
		}
	}
}
