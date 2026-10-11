package parser

import (
	"errors"
	"testing"
)

// TestSetTimeZoneIntervalFlattened pins gram.y zone_value's ConstInterval
// arms. flatten_set_variable_args coerces the TypeCast through interval_in
// with the qualifier's typmod and stores INTERVAL '<interval_out>', which is
// the text check_timezone later parses; storing the statement text verbatim
// made SHOW TimeZone echo it back. Expected values are what PG 18.3 hands
// check_timezone (visible in pg_proc.proconfig for a function SET clause).
func TestSetTimeZoneIntervalFlattened(t *testing.T) {
	for _, c := range []struct{ sql, want string }{
		{"SET TIME ZONE INTERVAL '+02:00' HOUR TO MINUTE", "INTERVAL '02:00:00'"},
		{"SET TIME ZONE INTERVAL '-08:00' HOUR TO MINUTE", "INTERVAL '-08:00:00'"},
		{"SET TIME ZONE INTERVAL '05:30:15'", "INTERVAL '05:30:15'"},
		// a bare number takes the qualifier's unit; HOUR TO MINUTE truncates
		{"SET TIME ZONE INTERVAL '3' HOUR", "INTERVAL '03:00:00'"},
		{"SET TIME ZONE INTERVAL '3' MINUTE", "INTERVAL '00:03:00'"},
		{"SET TIME ZONE INTERVAL '1:02:03' HOUR TO MINUTE", "INTERVAL '01:02:00'"},
		// INTERVAL(p) is the full range with a seconds precision
		{"SET TIME ZONE INTERVAL(2) '1:00:30.555'", "INTERVAL '01:00:30.56'"},
		// months and days survive the flattening; check_timezone rejects them
		{"SET TIME ZONE INTERVAL '1 day'", "INTERVAL '1 day'"},
		{"SET LOCAL TIME ZONE INTERVAL '+01:00' HOUR TO MINUTE", "INTERVAL '01:00:00'"},
	} {
		s, ok := parseOne(t, c.sql).(*SetStmt)
		if !ok {
			t.Fatalf("%s: not a SetStmt", c.sql)
		}
		if s.Name != "timezone" || s.Value != c.want || s.Default {
			t.Errorf("%s: Name=%q Value=%q Default=%v; want timezone %q", c.sql, s.Name, s.Value, s.Default, c.want)
		}
	}
}

// TestSetTimeZoneLocalAndDefault: zone_value's LOCAL and DEFAULT are both a
// NULL value, which gram.y turns into VAR_SET_DEFAULT.
func TestSetTimeZoneLocalAndDefault(t *testing.T) {
	for _, sql := range []string{"SET TIME ZONE LOCAL", "SET TIME ZONE DEFAULT", "SET LOCAL TIME ZONE LOCAL"} {
		s := parseOne(t, sql).(*SetStmt)
		if !s.Default || s.Name != "timezone" {
			t.Errorf("%s: Default=%v Name=%q; want a reset of timezone", sql, s.Default, s.Name)
		}
	}
}

// TestSetTimeZoneIntervalQualifierRejected: only HOUR, MINUTE and HOUR TO
// MINUTE are allowed (gram.y zone_value), with the error at the qualifier.
func TestSetTimeZoneIntervalQualifierRejected(t *testing.T) {
	for _, c := range []struct {
		sql string
		pos int
	}{
		{"SET TIME ZONE INTERVAL '3' DAY", 27},
		{"SET TIME ZONE INTERVAL '3' HOUR TO SECOND", 27},
	} {
		_, err := Parse(c.sql)
		var se *SyntaxError
		if !errors.As(err, &se) {
			t.Fatalf("%s: err = %v; want a SyntaxError", c.sql, err)
		}
		if se.Message != "time zone interval must be HOUR or HOUR TO MINUTE" {
			t.Errorf("%s: message %q", c.sql, se.Message)
		}
		if se.Pos != c.pos {
			t.Errorf("%s: position %d; want %d (0-based: PG's caret is at the qualifier)", c.sql, se.Pos, c.pos)
		}
	}
}
