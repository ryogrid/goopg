package wal

import (
	"reflect"
	"testing"
)

// TestEncodeDecodeCreateAggregateRoundTrip pins the DU-002
// restart-persistence (M0119-0004, slice 405 ledger resume point (c))
// CREATE AGGREGATE WAL record format. Encode -> Decode must return the
// original name/sType/sFunc/finalFunc/combineFunc/initCond/finalFuncModify/
// argTypes/OID/strict-flag/variadic-flag.
func TestEncodeDecodeCreateAggregateRoundTrip(t *testing.T) {
	cases := []struct {
		name, sType, sFunc, finalFunc, combineFunc, initCond, finalFuncModify string
		argTypes                                                             []string
		oid                                                                  uint32
		sFuncStrict, variadic                                                bool
	}{
		{"newavg", "_avgstate", "avg_transfn", "avg_finalfn", "", "", "", []string{"int4"}, 16384, true, false},
		{"myvariadicagg", "int8", "int8pl", "", "int8pl", "0", "read_write", []string{"int8"}, 16385, false, true},
		{"zeroarg", "int8", "myinc", "", "", "", "", nil, 16386, false, false},
		{"日本語集約", "text", "concat_state", "concat_final", "", "", "", []string{"text", "text"}, 4294967295, true, false}, // multi-byte UTF-8, max OID
	}
	for _, c := range cases {
		raw := EncodeCreateAggregate(c.name, c.sType, c.sFunc, c.finalFunc, c.combineFunc, c.initCond, c.finalFuncModify, c.argTypes, c.oid, c.sFuncStrict, c.variadic)
		if raw[0] != RecordKindCreateAggregate {
			t.Errorf("%q: kind byte = %d, want %d", c.name, raw[0], RecordKindCreateAggregate)
			continue
		}
		gotName, gotSType, gotSFunc, gotFinalFunc, gotCombineFunc, gotInitCond, gotFinalFuncModify, gotArgTypes, gotOID, gotStrict, gotVariadic, err := DecodeCreateAggregate(raw)
		if err != nil {
			t.Errorf("%q: decode err: %v", c.name, err)
			continue
		}
		if gotName != c.name || gotSType != c.sType || gotSFunc != c.sFunc || gotFinalFunc != c.finalFunc ||
			gotCombineFunc != c.combineFunc || gotInitCond != c.initCond || gotFinalFuncModify != c.finalFuncModify ||
			gotOID != c.oid || gotStrict != c.sFuncStrict || gotVariadic != c.variadic {
			t.Errorf("%q: decoded (%q, %q, %q, %q, %q, %q, %q, %d, %v, %v)",
				c.name, gotName, gotSType, gotSFunc, gotFinalFunc, gotCombineFunc, gotInitCond, gotFinalFuncModify, gotOID, gotStrict, gotVariadic)
		}
		if !reflect.DeepEqual(gotArgTypes, c.argTypes) && !(len(gotArgTypes) == 0 && len(c.argTypes) == 0) {
			t.Errorf("%q: ArgTypes = %+v, want %+v", c.name, gotArgTypes, c.argTypes)
		}
	}
}

// TestEncodeDecodeAlterAggregateRenameRoundTrip is the ALTER ... RENAME TO
// counterpart.
func TestEncodeDecodeAlterAggregateRenameRoundTrip(t *testing.T) {
	cases := []struct{ name, newName string }{
		{"newavg", "renamedavg"},
		{"日本語", "新しい名前"},
	}
	for _, c := range cases {
		raw := EncodeAlterAggregateRename(c.name, c.newName)
		if raw[0] != RecordKindAlterAggregateRename {
			t.Errorf("%q: kind byte = %d, want %d", c.name, raw[0], RecordKindAlterAggregateRename)
			continue
		}
		gotName, gotNewName, err := DecodeAlterAggregateRename(raw)
		if err != nil {
			t.Errorf("%q: decode err: %v", c.name, err)
			continue
		}
		if gotName != c.name || gotNewName != c.newName {
			t.Errorf("%q: decoded (%q, %q), want (%q, %q)", c.name, gotName, gotNewName, c.name, c.newName)
		}
	}
}

// TestDecodeCreateAggregateRejectsTruncatedPayload guards against a panic on
// a corrupt/truncated WAL record.
func TestDecodeCreateAggregateRejectsTruncatedPayload(t *testing.T) {
	full := EncodeCreateAggregate("agg", "int4", "sfunc", "", "", "", "", []string{"int4"}, 100, false, false)
	for n := 0; n < len(full); n++ {
		if _, _, _, _, _, _, _, _, _, _, _, err := DecodeCreateAggregate(full[:n]); err == nil && n < len(full) {
			t.Errorf("truncated to %d bytes: want error, got nil", n)
		}
	}
}

// TestEncodeDecodeDropAggregateRoundTrip pins the DROP AGGREGATE WAL record
// format (loop #56 ledger resume point: DROP AGGREGATE restart persistence).
func TestEncodeDecodeDropAggregateRoundTrip(t *testing.T) {
	cases := []string{"newavg", "myvariadicagg", "日本語集約"}
	for _, name := range cases {
		raw := EncodeDropAggregate(name)
		if raw[0] != RecordKindDropAggregate {
			t.Errorf("%q: kind byte = %d, want %d", name, raw[0], RecordKindDropAggregate)
			continue
		}
		gotName, err := DecodeDropAggregate(raw)
		if err != nil {
			t.Errorf("%q: decode err: %v", name, err)
			continue
		}
		if gotName != name {
			t.Errorf("decoded %q, want %q", gotName, name)
		}
	}
}

// TestDecodeDropAggregateRejectsTruncatedPayload guards against a panic on a
// corrupt/truncated WAL record.
func TestDecodeDropAggregateRejectsTruncatedPayload(t *testing.T) {
	full := EncodeDropAggregate("agg")
	for n := 0; n < len(full); n++ {
		if _, err := DecodeDropAggregate(full[:n]); err == nil {
			t.Errorf("truncated to %d bytes: want error, got nil", n)
		}
	}
}

// TestEncodeDecodeAlterAggregateOwnerRoundTrip pins the ALTER AGGREGATE ...
// OWNER TO WAL record format (loop #57 ledger resume point: ALTER AGGREGATE
// OWNER TO restart persistence).
func TestEncodeDecodeAlterAggregateOwnerRoundTrip(t *testing.T) {
	cases := []struct {
		name     string
		ownerOID uint32
	}{
		{"newavg", 16400},
		{"日本語集約", 10},
		{"maxowner", 4294967295},
	}
	for _, c := range cases {
		raw := EncodeAlterAggregateOwner(c.name, c.ownerOID)
		if raw[0] != RecordKindAlterAggregateOwner {
			t.Errorf("%q: kind byte = %d, want %d", c.name, raw[0], RecordKindAlterAggregateOwner)
			continue
		}
		gotName, gotOwnerOID, err := DecodeAlterAggregateOwner(raw)
		if err != nil {
			t.Errorf("%q: decode err: %v", c.name, err)
			continue
		}
		if gotName != c.name || gotOwnerOID != c.ownerOID {
			t.Errorf("%q: decoded (%q, %d), want (%q, %d)", c.name, gotName, gotOwnerOID, c.name, c.ownerOID)
		}
	}
}

// TestDecodeAlterAggregateOwnerRejectsTruncatedPayload guards against a
// panic on a corrupt/truncated WAL record.
func TestDecodeAlterAggregateOwnerRejectsTruncatedPayload(t *testing.T) {
	full := EncodeAlterAggregateOwner("agg", 100)
	for n := 0; n < len(full); n++ {
		if _, _, err := DecodeAlterAggregateOwner(full[:n]); err == nil {
			t.Errorf("truncated to %d bytes: want error, got nil", n)
		}
	}
}
