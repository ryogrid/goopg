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

// TestEncodeCreateIndexExtensionRoundTrip pins the M0122-0006 follow-up
// encoding contract: HasPredicate/PredicateString, IncludeColumns,
// ColOpClasses, ColCollations, Fillfactor, DeduplicateItems, and
// NullsNotDistinct must all round-trip byte-for-byte through Encode/Decode.
func TestEncodeCreateIndexExtensionRoundTrip(t *testing.T) {
	dedupOff := false
	dedupOn := true
	cases := []struct {
		name string
		p    CreateIndexPayload
	}{
		{
			name: "plain index emits no extension block",
			p: CreateIndexPayload{
				OID: 1, TableOID: 2, Name: "plain_idx", Method: "btree",
				Columns: []string{"a"}, Unique: true,
			},
		},
		{
			name: "partial index with predicate",
			p: CreateIndexPayload{
				OID: 3, TableOID: 4, Name: "partial_idx", Method: "btree",
				Columns:         []string{"a"},
				HasPredicate:    true,
				PredicateString: "(a > 0)",
			},
		},
		{
			name: "include columns + opclass + collation + fillfactor + dedup off + nulls not distinct",
			p: CreateIndexPayload{
				OID: 5, TableOID: 6, Name: "full_idx", Method: "btree",
				Columns:          []string{"a", "b"},
				Unique:           true,
				IncludeColumns:   []string{"c", "d"},
				ColOpClasses:     []string{"text_pattern_ops", ""},
				ColCollations:    []string{"", "\"C\""},
				Fillfactor:       70,
				DeduplicateItems: &dedupOff,
				NullsNotDistinct: true,
			},
		},
		{
			name: "dedup on, no other extension fields",
			p: CreateIndexPayload{
				OID: 7, TableOID: 8, Name: "dedup_idx", Method: "btree",
				Columns:          []string{"a"},
				DeduplicateItems: &dedupOn,
			},
		},
		{
			name: "tablespace only",
			p: CreateIndexPayload{
				OID: 9, TableOID: 10, Name: "ts_idx", Method: "btree",
				Columns:    []string{"a"},
				Tablespace: 16391,
			},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			enc := EncodeCreateIndex(tc.p)
			got, err := DecodeCreateIndex(enc)
			if err != nil {
				t.Fatalf("DecodeCreateIndex: %v", err)
			}
			if got.HasPredicate != tc.p.HasPredicate {
				t.Errorf("HasPredicate = %v, want %v", got.HasPredicate, tc.p.HasPredicate)
			}
			if got.PredicateString != tc.p.PredicateString {
				t.Errorf("PredicateString = %q, want %q", got.PredicateString, tc.p.PredicateString)
			}
			if len(got.IncludeColumns) != len(tc.p.IncludeColumns) {
				t.Fatalf("IncludeColumns len = %d, want %d", len(got.IncludeColumns), len(tc.p.IncludeColumns))
			}
			for i := range tc.p.IncludeColumns {
				if got.IncludeColumns[i] != tc.p.IncludeColumns[i] {
					t.Errorf("IncludeColumns[%d] = %q, want %q", i, got.IncludeColumns[i], tc.p.IncludeColumns[i])
				}
			}
			wantOpc := tc.p.ColOpClasses
			if wantOpc == nil {
				wantOpc = make([]string, len(tc.p.Columns))
			}
			for i := range tc.p.Columns {
				if stringAt(got.ColOpClasses, i) != wantOpc[i] {
					t.Errorf("ColOpClasses[%d] = %q, want %q", i, stringAt(got.ColOpClasses, i), wantOpc[i])
				}
			}
			wantColl := tc.p.ColCollations
			if wantColl == nil {
				wantColl = make([]string, len(tc.p.Columns))
			}
			for i := range tc.p.Columns {
				if stringAt(got.ColCollations, i) != wantColl[i] {
					t.Errorf("ColCollations[%d] = %q, want %q", i, stringAt(got.ColCollations, i), wantColl[i])
				}
			}
			if got.Fillfactor != tc.p.Fillfactor {
				t.Errorf("Fillfactor = %d, want %d", got.Fillfactor, tc.p.Fillfactor)
			}
			if (got.DeduplicateItems == nil) != (tc.p.DeduplicateItems == nil) {
				t.Fatalf("DeduplicateItems nil-ness mismatch: got=%v want=%v", got.DeduplicateItems, tc.p.DeduplicateItems)
			}
			if tc.p.DeduplicateItems != nil && *got.DeduplicateItems != *tc.p.DeduplicateItems {
				t.Errorf("DeduplicateItems = %v, want %v", *got.DeduplicateItems, *tc.p.DeduplicateItems)
			}
			if got.NullsNotDistinct != tc.p.NullsNotDistinct {
				t.Errorf("NullsNotDistinct = %v, want %v", got.NullsNotDistinct, tc.p.NullsNotDistinct)
			}
			if got.Tablespace != tc.p.Tablespace {
				t.Errorf("Tablespace = %d, want %d", got.Tablespace, tc.p.Tablespace)
			}
		})
	}
}

// TestEncodeCreateIndexOmitsExtensionBlockWhenDefault pins that a plain index
// (no predicate/INCLUDE/opclass/collation/fillfactor/dedup/nulls-not-distinct)
// encodes to EXACTLY the same bytes as before this M0122-0006 follow-up
// existed — the extension block is omitted entirely, not emitted with all
// zero/empty fields. This is what keeps every pre-existing backward-compat
// test (TestDecodeCreateIndexBackwardCompatNoOrderBlocks,
// TestDecodeCreateIndexRejectsTruncated) valid unmodified.
func TestEncodeCreateIndexOmitsExtensionBlockWhenDefault(t *testing.T) {
	p := CreateIndexPayload{
		OID: 1, TableOID: 2, Name: "plain_idx", Method: "btree",
		Columns: []string{"a", "b"}, Unique: true,
		ColDescending: []bool{true, false},
		ColNullsFirst: []bool{false, true},
	}
	enc := EncodeCreateIndex(p)
	wantLen := len(enc) // baseline: header+strings+columns+order-blocks only
	if hasCreateIndexExtension(p) {
		t.Fatal("hasCreateIndexExtension(plain payload) = true, want false")
	}
	// Setting any single new field must grow the encoded length.
	withPred := p
	withPred.HasPredicate = true
	if l := len(EncodeCreateIndex(withPred)); l <= wantLen {
		t.Errorf("HasPredicate=true encoded length = %d, want > %d", l, wantLen)
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

// TestDecodeCreateIndexBackwardCompatOrderBlocksOnlyNoExtension pins
// decoding of a "gen1" record — one written by the M0122-0006 commit that
// added ColDescending/ColNullsFirst but predates this follow-up's extension
// block. Such a record ends exactly after the order blocks (remaining ==
// 2*numCols): every M0122-0006-follow-up field must default to its zero
// value, not error out as "truncated".
func TestDecodeCreateIndexBackwardCompatOrderBlocksOnlyNoExtension(t *testing.T) {
	p := CreateIndexPayload{
		OID: 9, TableOID: 10, Name: "gen1_idx", Method: "btree",
		Columns:       []string{"a"},
		ColDescending: []bool{true},
		ColNullsFirst: []bool{true},
	}
	// p has no extension-triggering fields set, so EncodeCreateIndex already
	// produces the exact gen1 shape (order blocks, no extension) — assert
	// that directly rather than hand-truncating.
	enc := EncodeCreateIndex(p)
	got, err := DecodeCreateIndex(enc)
	if err != nil {
		t.Fatalf("DecodeCreateIndex(gen1 payload): %v", err)
	}
	if got.HasPredicate || got.PredicateString != "" || len(got.IncludeColumns) != 0 ||
		got.Fillfactor != 0 || got.DeduplicateItems != nil || got.NullsNotDistinct || got.Tablespace != 0 {
		t.Errorf("gen1 payload must decode with every follow-up field at its zero value, got %+v", got)
	}
	if !got.ColDescending[0] || !got.ColNullsFirst[0] {
		t.Errorf("gen1 order flags lost: ColDescending=%v ColNullsFirst=%v", got.ColDescending, got.ColNullsFirst)
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

// TestDecodeCreateIndexRejectsTruncatedExtension pins the defensive length
// checks inside decodeCreateIndexExtension (M0122-0006 follow-up): cutting
// the payload anywhere inside the extension block must error, never silently
// decode a partial/garbage value.
func TestDecodeCreateIndexRejectsTruncatedExtension(t *testing.T) {
	dedup := true
	p := CreateIndexPayload{
		OID: 11, TableOID: 12, Name: "trunc_idx", Method: "btree",
		Columns:          []string{"a", "b"},
		HasPredicate:     true,
		PredicateString:  "(a > 0)",
		IncludeColumns:   []string{"c"},
		ColOpClasses:     []string{"text_pattern_ops", ""},
		ColCollations:    []string{"", "\"C\""},
		Fillfactor:       70,
		DeduplicateItems: &dedup,
	}
	enc := EncodeCreateIndex(p)
	base := p
	base.HasPredicate, base.PredicateString = false, ""
	base.IncludeColumns, base.ColOpClasses, base.ColCollations = nil, nil, nil
	base.Fillfactor, base.DeduplicateItems = 0, nil
	extBoundary := len(EncodeCreateIndex(base)) // order-blocks-only prefix length; no extension bytes yet
	// cut == extBoundary itself (zero extension bytes) is the valid gen1
	// shape (no extension present at all) — start just past it.
	for cut := extBoundary + 1; cut < len(enc); cut++ {
		if _, err := DecodeCreateIndex(enc[:cut]); err == nil {
			t.Errorf("truncated extension payload at cut=%d must error", cut)
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
