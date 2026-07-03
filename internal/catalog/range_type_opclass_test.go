package catalog

import (
	"errors"
	"testing"
)

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
			rt, err := c.RegisterRangeType(tc.rangeName, tc.subtype, "", "", "")
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
	_, err := c.RegisterRangeType("jsonrange", "json", "", "", "")
	if err == nil {
		t.Fatal("RegisterRangeType(\"jsonrange\", \"json\") expected an error, got nil")
	}
	const want = `data type json has no default operator class for access method "btree"`
	if err.Error() != want {
		t.Errorf("RegisterRangeType error = %q, want %q", err.Error(), want)
	}
}

// TestRegisterRangeTypeExplicitOpclass exercises the DU-002 (M0110-0001,
// slice 429 follow-up sub-item (a)) `subtype_opclass` option: a named
// built-in opclass that accepts the subtype is used verbatim (not just the
// default), a named opclass whose opcintype doesn't accept the subtype is
// rejected with PG's ERRCODE_DATATYPE_MISMATCH wording, an unknown name is
// rejected with ERRCODE_UNDEFINED_OBJECT wording, and a user-created (CREATE
// OPERATOR CLASS) btree opclass for the subtype resolves too.
func TestRegisterRangeTypeExplicitOpclass(t *testing.T) {
	t.Run("named builtin opclass matching subtype", func(t *testing.T) {
		c := NewInMemory()
		rt, err := c.RegisterRangeType("myrange", "int4", "", "int4_ops", "")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if rt.OpclassOID != 1978 {
			t.Errorf("OpclassOID = %d, want 1978 (int4_ops)", rt.OpclassOID)
		}
	})
	t.Run("named builtin opclass datatype mismatch", func(t *testing.T) {
		c := NewInMemory()
		_, err := c.RegisterRangeType("myrange", "int4", "", "text_ops", "")
		if err == nil {
			t.Fatal("expected an error, got nil")
		}
		var rte *RangeTypeOptionError
		if !errors.As(err, &rte) {
			t.Fatalf("error is not *RangeTypeOptionError: %v (%T)", err, err)
		}
		if rte.Code != "42804" {
			t.Errorf("Code = %q, want 42804", rte.Code)
		}
		const want = `operator class "text_ops" does not accept data type int4`
		if err.Error() != want {
			t.Errorf("error = %q, want %q", err.Error(), want)
		}
	})
	t.Run("unknown opclass name", func(t *testing.T) {
		c := NewInMemory()
		_, err := c.RegisterRangeType("myrange", "int4", "", "nope_ops", "")
		if err == nil {
			t.Fatal("expected an error, got nil")
		}
		var rte *RangeTypeOptionError
		if !errors.As(err, &rte) {
			t.Fatalf("error is not *RangeTypeOptionError: %v (%T)", err, err)
		}
		if rte.Code != "42704" {
			t.Errorf("Code = %q, want 42704", rte.Code)
		}
	})
	t.Run("user-created btree opclass for the subtype", func(t *testing.T) {
		c := NewInMemory()
		uoc := c.RegisterUserOperatorClass("public", "my_int4_ops", PublicNamespaceOID, 10, btreeAccessMethodOID, 0, OIDInt4, false, 0)
		rt, err := c.RegisterRangeType("myrange", "int4", "", "my_int4_ops", "")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if rt.OpclassOID != uoc.OID {
			t.Errorf("OpclassOID = %d, want %d (my_int4_ops)", rt.OpclassOID, uoc.OID)
		}
	})
}

// TestRegisterRangeTypeUserDefaultOpclass exercises the DU-002 (M0110-0001,
// slice 429 follow-up sub-item (b)) generic-default-opclass gap: a subtype
// with no curated `builtinRangeSubtypeOpclasses` entry (json — PostgreSQL
// itself has no built-in btree opclass for it, confirmed by
// TestRegisterRangeTypeStillRejectsUnsupportedSubtype above) must still
// resolve via a user-created `CREATE OPERATOR CLASS ... DEFAULT` btree
// opclass when the range's `subtype_opclass` option is omitted, mirroring
// PostgreSQL's single `GetDefaultOpClass` pg_opclass scan (which does not
// distinguish builtin vs. user-created rows).
func TestRegisterRangeTypeUserDefaultOpclass(t *testing.T) {
	t.Run("user default opclass resolves the empty subtype_opclass option", func(t *testing.T) {
		c := NewInMemory()
		uoc := c.RegisterUserOperatorClass("public", "my_json_ops", PublicNamespaceOID, 10, btreeAccessMethodOID, 0, OIDJSON, true, 0)
		rt, err := c.RegisterRangeType("jsonrange", "json", "", "", "")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if rt.OpclassOID != uoc.OID {
			t.Errorf("OpclassOID = %d, want %d (my_json_ops)", rt.OpclassOID, uoc.OID)
		}
	})
	t.Run("non-default user opclass is not picked up implicitly", func(t *testing.T) {
		c := NewInMemory()
		c.RegisterUserOperatorClass("public", "my_json_ops", PublicNamespaceOID, 10, btreeAccessMethodOID, 0, OIDJSON, false, 0)
		_, err := c.RegisterRangeType("jsonrange", "json", "", "", "")
		if err == nil {
			t.Fatal("expected an error (no default opclass), got nil")
		}
		var rte *RangeTypeOptionError
		if !errors.As(err, &rte) {
			t.Fatalf("error is not *RangeTypeOptionError: %v (%T)", err, err)
		}
		if rte.Code != "42704" {
			t.Errorf("Code = %q, want 42704", rte.Code)
		}
	})
	t.Run("curated builtin default still wins over a user opclass for the same subtype", func(t *testing.T) {
		c := NewInMemory()
		c.RegisterUserOperatorClass("public", "my_int4_ops", PublicNamespaceOID, 10, btreeAccessMethodOID, 0, OIDInt4, true, 0)
		rt, err := c.RegisterRangeType("myrange", "int4", "", "", "")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if rt.OpclassOID != 1978 {
			t.Errorf("OpclassOID = %d, want 1978 (curated int4_ops)", rt.OpclassOID)
		}
	})
}

// TestRegisterRangeTypeExplicitCollation exercises the DU-002 (M0110-0001,
// slice 429 follow-up sub-item (a)) `collation` option: a built-in name
// resolves to its OID for a collatable subtype, a user-created (CREATE
// COLLATION) name resolves too, specifying one for a non-collatable subtype
// (e.g. int4) is rejected (ERRCODE_WRONG_OBJECT_TYPE), and an unknown name is
// rejected (ERRCODE_UNDEFINED_OBJECT). With no explicit collation, a
// collatable subtype still gets the DEFAULT_COLLATION_OID (100) it always
// had, and a non-collatable subtype gets 0 (InvalidOid).
func TestRegisterRangeTypeExplicitCollation(t *testing.T) {
	t.Run("builtin collation name on collatable subtype", func(t *testing.T) {
		c := NewInMemory()
		rt, err := c.RegisterRangeType("textrange", "text", "", "", "C")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if rt.CollationOID != 950 {
			t.Errorf("CollationOID = %d, want 950 (C)", rt.CollationOID)
		}
	})
	t.Run("no explicit collation defaults to DEFAULT_COLLATION_OID", func(t *testing.T) {
		c := NewInMemory()
		rt, err := c.RegisterRangeType("textrange", "text", "", "", "")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if rt.CollationOID != 100 {
			t.Errorf("CollationOID = %d, want 100 (default)", rt.CollationOID)
		}
	})
	t.Run("non-collatable subtype gets InvalidOid", func(t *testing.T) {
		c := NewInMemory()
		rt, err := c.RegisterRangeType("myrange", "int4", "", "", "")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if rt.CollationOID != 0 {
			t.Errorf("CollationOID = %d, want 0 (InvalidOid)", rt.CollationOID)
		}
	})
	t.Run("collation specified for non-collatable subtype rejected", func(t *testing.T) {
		c := NewInMemory()
		_, err := c.RegisterRangeType("myrange", "int4", "", "", "C")
		if err == nil {
			t.Fatal("expected an error, got nil")
		}
		var rte *RangeTypeOptionError
		if !errors.As(err, &rte) {
			t.Fatalf("error is not *RangeTypeOptionError: %v (%T)", err, err)
		}
		if rte.Code != "42809" {
			t.Errorf("Code = %q, want 42809", rte.Code)
		}
		const want = "range collation specified but subtype does not support collation"
		if err.Error() != want {
			t.Errorf("error = %q, want %q", err.Error(), want)
		}
	})
	t.Run("unknown collation name", func(t *testing.T) {
		c := NewInMemory()
		_, err := c.RegisterRangeType("textrange", "text", "", "", "nope_collation")
		if err == nil {
			t.Fatal("expected an error, got nil")
		}
		var rte *RangeTypeOptionError
		if !errors.As(err, &rte) {
			t.Fatalf("error is not *RangeTypeOptionError: %v (%T)", err, err)
		}
		if rte.Code != "42704" {
			t.Errorf("Code = %q, want 42704", rte.Code)
		}
	})
	t.Run("user-created collation name", func(t *testing.T) {
		c := NewInMemory()
		oid, cerr := c.CreateCollation(&UserCollation{Name: "my_coll", Provider: 'c', Collate: "C", Ctype: "C", Deterministic: true}, "public", false)
		if cerr != nil {
			t.Fatalf("CreateCollation: %v", cerr)
		}
		rt, err := c.RegisterRangeType("textrange", "text", "", "", "my_coll")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if rt.CollationOID != oid {
			t.Errorf("CollationOID = %d, want %d (my_coll)", rt.CollationOID, oid)
		}
	})
}
