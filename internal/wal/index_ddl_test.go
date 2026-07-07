package wal

import (
	"testing"
)

// TestEncodeCreateIndexRoundTrip pins the WAL encoding contract
// for `RecordKindCreateIndex` (M0079-0001). Decoding the encoded
// payload must yield byte-identical metadata, including
// non-default flags (Unique=true, Primary=true), multiple
// columns, and non-empty Schema/Method.
func TestEncodeCreateIndexRoundTrip(t *testing.T) {
	cases := []CreateIndexPayload{
		{
			OID:      16405,
			TableOID: 16402,
			Schema:   "",
			Name:     "pgbench_accounts_pkey",
			Method:   "btree",
			Columns:  []string{"aid"},
			Unique:   true,
			Primary:  true,
		},
		{
			OID:           99999,
			TableOID:      88888,
			Schema:        "public",
			Name:          "idx_partsupp_pk",
			Method:        "btree",
			Columns:       []string{"ps_partkey", "ps_suppkey"},
			Unique:        true,
			Primary:       true,
			ColDescending: []bool{true, false},
			ColNullsFirst: []bool{false, true},
		},
		{
			OID:      42,
			TableOID: 41,
			Schema:   "",
			Name:     "idx_lineitem_orderkey",
			Method:   "btree",
			Columns:  []string{"l_orderkey"},
			Unique:   false,
			Primary:  false,
		},
	}
	for _, want := range cases {
		t.Run(want.Name, func(t *testing.T) {
			enc := EncodeCreateIndex(want)
			if len(enc) == 0 {
				t.Fatal("EncodeCreateIndex returned empty payload")
			}
			if enc[0] != RecordKindCreateIndex {
				t.Fatalf("kind byte = %d, want %d", enc[0], RecordKindCreateIndex)
			}
			got, err := DecodeCreateIndex(enc)
			if err != nil {
				t.Fatalf("DecodeCreateIndex: %v", err)
			}
			if got.OID != want.OID || got.TableOID != want.TableOID {
				t.Errorf("OID/TableOID mismatch: got=%d/%d want=%d/%d", got.OID, got.TableOID, want.OID, want.TableOID)
			}
			if got.Schema != want.Schema || got.Name != want.Name || got.Method != want.Method {
				t.Errorf("string fields mismatch: got=(%q/%q/%q) want=(%q/%q/%q)",
					got.Schema, got.Name, got.Method, want.Schema, want.Name, want.Method)
			}
			if got.Unique != want.Unique || got.Primary != want.Primary {
				t.Errorf("flags mismatch: got=(unique=%v,primary=%v) want=(unique=%v,primary=%v)",
					got.Unique, got.Primary, want.Unique, want.Primary)
			}
			if len(got.Columns) != len(want.Columns) {
				t.Fatalf("Columns len mismatch: got=%d want=%d", len(got.Columns), len(want.Columns))
			}
			for i := range want.Columns {
				if got.Columns[i] != want.Columns[i] {
					t.Errorf("Columns[%d] mismatch: got=%q want=%q", i, got.Columns[i], want.Columns[i])
				}
			}
			wantDesc := want.ColDescending
			wantNullsFirst := want.ColNullsFirst
			if wantDesc == nil {
				wantDesc = make([]bool, len(want.Columns))
			}
			if wantNullsFirst == nil {
				wantNullsFirst = make([]bool, len(want.Columns))
			}
			for i := range want.Columns {
				if got.ColDescending[i] != wantDesc[i] {
					t.Errorf("ColDescending[%d] mismatch: got=%v want=%v", i, got.ColDescending[i], wantDesc[i])
				}
				if got.ColNullsFirst[i] != wantNullsFirst[i] {
					t.Errorf("ColNullsFirst[%d] mismatch: got=%v want=%v", i, got.ColNullsFirst[i], wantNullsFirst[i])
				}
			}
		})
	}
}

// TestDecodeCreateIndexBackwardCompatNoOrderBlocks pins M0122-0006's
// backward-compatibility contract: a pre-existing on-disk WAL record
// encoded before ColDescending/ColNullsFirst existed (i.e. missing the two
// trailing order-flag blocks entirely) must still decode successfully,
// defaulting every column to ascending/NULLS LAST — the exact behavior
// that record's original CREATE INDEX actually had.
func TestDecodeCreateIndexBackwardCompatNoOrderBlocks(t *testing.T) {
	p := CreateIndexPayload{
		OID: 7, TableOID: 8, Name: "old_idx", Method: "btree",
		Columns: []string{"a", "b"}, Unique: true,
		ColDescending: []bool{true, true}, // must be ignored — sliced off below
	}
	enc := EncodeCreateIndex(p)
	oldFormat := enc[:len(enc)-2*len(p.Columns)]

	got, err := DecodeCreateIndex(oldFormat)
	if err != nil {
		t.Fatalf("DecodeCreateIndex(old-format payload): %v", err)
	}
	if len(got.Columns) != 2 || got.Columns[0] != "a" || got.Columns[1] != "b" {
		t.Fatalf("Columns = %v, want [a b]", got.Columns)
	}
	for i, want := range []bool{false, false} {
		if got.ColDescending[i] != want {
			t.Errorf("ColDescending[%d] = %v, want %v (old-format record must default to ascending)", i, got.ColDescending[i], want)
		}
		if got.ColNullsFirst[i] != want {
			t.Errorf("ColNullsFirst[%d] = %v, want %v (old-format record must default to NULLS LAST)", i, got.ColNullsFirst[i], want)
		}
	}
}

// TestDecodeCreateIndexRejectsTruncated pins the defensive
// length checks in DecodeCreateIndex. (M0079-0001.)
//
// One cut point is deliberately excluded: len(enc) minus the trailing
// ColDescending/ColNullsFirst blocks (2*numCols bytes) is, by construction,
// byte-identical to a legitimate pre-M0122-0006 record for the same
// index (which never had those blocks at all) — DecodeCreateIndex must
// accept that shape for backward compatibility with existing on-disk WAL,
// so it cannot also be treated as "truncated" here.
func TestDecodeCreateIndexRejectsTruncated(t *testing.T) {
	p := CreateIndexPayload{
		OID: 100, TableOID: 99, Name: "idx", Method: "btree",
		Columns: []string{"col"}, Unique: true,
	}
	enc := EncodeCreateIndex(p)
	oldFormatBoundary := len(enc) - 2*len(p.Columns)
	for cut := 0; cut < len(enc); cut++ {
		if cut == oldFormatBoundary {
			continue
		}
		if _, err := DecodeCreateIndex(enc[:cut]); err == nil {
			t.Errorf("truncated payload at cut=%d must error", cut)
		}
	}
}

// TestDecodeCreateIndexRejectsWrongKind pins the kind-byte
// guard. (M0079-0001.)
func TestDecodeCreateIndexRejectsWrongKind(t *testing.T) {
	bogus := EncodeCreateIndex(CreateIndexPayload{
		OID: 1, TableOID: 2, Name: "x", Method: "btree", Columns: []string{"c"},
	})
	bogus[0] = RecordKindHeapInsert // wrong kind byte
	if _, err := DecodeCreateIndex(bogus); err == nil {
		t.Error("DecodeCreateIndex must reject non-CreateIndex kind bytes")
	}
}

// TestEncodeDropIndexRoundTrip pins the DROP INDEX
// encoding/decoding contract. (M0079-0001.)
func TestEncodeDropIndexRoundTrip(t *testing.T) {
	cases := []DropIndexPayload{
		{OID: 16405, Schema: "", Name: "pgbench_accounts_pkey"},
		{OID: 99, Schema: "public", Name: "idx_supplier_nationkey"},
	}
	for _, want := range cases {
		t.Run(want.Name, func(t *testing.T) {
			enc := EncodeDropIndex(want)
			if enc[0] != RecordKindDropIndex {
				t.Fatalf("kind byte = %d, want %d", enc[0], RecordKindDropIndex)
			}
			got, err := DecodeDropIndex(enc)
			if err != nil {
				t.Fatalf("DecodeDropIndex: %v", err)
			}
			if got != want {
				t.Errorf("payload mismatch: got=%+v want=%+v", got, want)
			}
		})
	}
}

// TestApplyRecordSkipsCreateAndDropIndex pins the M0079-0001
// physical-replay contract: the record kinds are recognised but
// applied as no-ops (catalog-only, replayed via the recovery
// driver in `internal/initdb`).
func TestApplyRecordSkipsCreateAndDropIndex(t *testing.T) {
	create := EncodeCreateIndex(CreateIndexPayload{
		OID: 1, TableOID: 2, Name: "x", Method: "btree", Columns: []string{"c"},
	})
	drop := EncodeDropIndex(DropIndexPayload{OID: 1, Name: "x"})
	for _, payload := range [][]byte{create, drop} {
		applied, err := ApplyRecord(nil, Record{Payload: payload})
		if err != nil {
			t.Errorf("ApplyRecord must not error on catalog-only DDL: %v", err)
		}
		if applied {
			t.Error("ApplyRecord must report applied=false for catalog-only DDL records")
		}
	}
}
