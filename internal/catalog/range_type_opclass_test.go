package catalog

import "testing"

// TestDefaultBtreeOpclassForSubtypeExpandedCoverage verifies the DU-002
// (M0110-0001) follow-up widening builtinRangeSubtypeOpclasses from the
// original 7 entries (the five built-in range types' subtypes plus text) to
// every PG18 built-in scalar type with a real default btree opclass. Every
// {subtype, opclass OID, family OID} triple was captured empirically from a
// live `postgres/local_install` PG 18.3 instance (see the map's doc comment)
// — this test pins those values so a future edit can't silently drift from
// the real catalog.
func TestDefaultBtreeOpclassForSubtypeExpandedCoverage(t *testing.T) {
	cases := []struct {
		name       string
		subtypeOID uint32
		opclassOID uint32
	}{
		{"int2", OIDInt2, 1979},
		{"int4", OIDInt4, 1978},
		{"int8", OIDInt8, 3124},
		{"numeric", OIDNumeric, 3125},
		{"float4", OIDFloat4, 10012},
		{"float8", OIDFloat8, 3123},
		{"date", OIDDate, 3122},
		{"time", OIDTime, 10038},
		{"timetz", OIDTimeTZ, 10041},
		{"timestamp", OIDTimestamp, 3128},
		{"timestamptz", OIDTimestampTZ, 3127},
		{"interval", OIDInterval, 10022},
		{"text", OIDText, 3126},
		{"varchar (falls back to text_ops — no default varchar_ops)", OIDVarChar, 3126},
		{"bpchar", OIDBpChar, 10004},
		{"name", OIDName, 10028},
		{"char", OIDChar, 10007},
		{"bool", OIDBool, 10003},
		{"bytea", OIDBytea, 10006},
		{"oid", OIDOID, 1981},
		{"tid", OIDTid, 10050},
		{"oidvector", OIDOidvector, 10032},
		{"uuid", OIDUUID, 10065},
		{"pg_lsn", OIDPgLsn, 10067},
		{"xid8", OIDXid8, 10053},
		{"money", OIDMoney, 10047},
		{"bit", OIDBit, 10002},
		{"varbit", OIDVarbit, 10043},
		{"macaddr", OIDMacaddr, 10024},
		{"macaddr8", OIDMacaddr8, 10026},
		{"inet", OIDInet, 10015},
		{"cidr (falls back to inet_ops — binary-coercible)", OIDCidr, 10015},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := DefaultBtreeOpclassForSubtype(tc.subtypeOID)
			if !ok {
				t.Fatalf("DefaultBtreeOpclassForSubtype(%d) = not found, want opclass %d", tc.subtypeOID, tc.opclassOID)
			}
			if got != tc.opclassOID {
				t.Errorf("DefaultBtreeOpclassForSubtype(%d) = %d, want %d", tc.subtypeOID, got, tc.opclassOID)
			}
		})
	}
}

// TestRegisterRangeTypeExpandedSubtypes verifies RegisterRangeType now
// succeeds for subtypes beyond the original int4/int8/numeric/date/
// timestamp/timestamptz/text set, and that builtinOpclassRowByOID renders a
// pg_opclass row for the resolved opclass (needed by pg_dump's dumpRangeType
// join).
func TestRegisterRangeTypeExpandedSubtypes(t *testing.T) {
	subtypes := []struct {
		rangeName string
		subtype   string
	}{
		{"boolrange", "bool"},
		{"float8range", "float8"},
		{"uuidrange", "uuid"},
		{"varcharrange", "varchar"},
		{"bpcharrange", "bpchar"},
		{"namerange", "name"},
	}
	for _, tc := range subtypes {
		t.Run(tc.rangeName, func(t *testing.T) {
			c := NewInMemory()
			rt, err := c.RegisterRangeType(tc.rangeName, tc.subtype, "")
			if err != nil {
				t.Fatalf("RegisterRangeType(%q, %q) unexpected error: %v", tc.rangeName, tc.subtype, err)
			}
			if rt.OpclassOID == 0 {
				t.Fatalf("RegisterRangeType(%q, %q) left OpclassOID unset", tc.rangeName, tc.subtype)
			}
			if _, ok := builtinOpclassRowByOID(rt.OpclassOID); !ok {
				t.Errorf("builtinOpclassRowByOID(%d) not found for resolved opclass of subtype %q", rt.OpclassOID, tc.subtype)
			}
		})
	}
}

// TestRegisterRangeTypeStillRejectsUnsupportedSubtype pins the negative case:
// a subtype with no real PG default btree opclass at all (e.g. json, which
// PostgreSQL itself has no btree opclass for) still reports PG's own
// ERRCODE_UNDEFINED_OBJECT wording rather than silently registering a broken
// range.
func TestRegisterRangeTypeStillRejectsUnsupportedSubtype(t *testing.T) {
	c := NewInMemory()
	_, err := c.RegisterRangeType("jsonrange", "json", "")
	if err == nil {
		t.Fatal("RegisterRangeType(\"jsonrange\", \"json\") expected an error, got nil")
	}
	const want = `data type json has no default operator class for access method "btree"`
	if err.Error() != want {
		t.Errorf("RegisterRangeType error = %q, want %q", err.Error(), want)
	}
}
