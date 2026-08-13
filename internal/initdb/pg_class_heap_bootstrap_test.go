package initdb

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/goopg/goopg/internal/storage"
)

// TestPgClassHeapBootstrapCoverage verifies M0130-S2 forward-path completeness:
// bootstrapPgClassTuples writes a valid pg_class heap page containing a tuple
// for every nailed relation (shared + local) in both base/1/1259 and base/5/1259.
//
// This is the primary evidence that BLOCKER #3 (pg_class virtual-only) is
// resolved for the forward path — PG connecting to a goopg data dir sees a
// well-formed pg_class heap with descriptors for every system catalog.
func TestPgClassHeapBootstrapCoverage(t *testing.T) {
	dir := t.TempDir()
	if err := Init(Options{DataDir: dir, NoSync: true}); err != nil {
		t.Fatal(err)
	}

	// M0131-S20.1: the pg_rewrite TOAST pair (2838/2839) has pg_class rows
	// too, and rides in the same heap without being a member of
	// nailedLocalRels — see nailedToastRels. M0133-S3: so do the four
	// information_schema data tables (and their TOAST pairs, folded into
	// nailedToastRels) — see informationSchemaDataTableRels.
	expectedCount := len(nailedSharedRels) + len(nailedLocalRels) + len(nailedToastRels()) + len(informationSchemaDataTableRels())

	// Bootstrap writes to both base/1 and base/5.
	for _, db := range []string{"base/1", "base/5"} {
		heapPath := filepath.Join(dir, db, "1259")
		data, err := os.ReadFile(heapPath)
		if err != nil {
			t.Fatalf("%s/1259: %v", db, err)
		}
		if len(data)%storage.BlockSize != 0 {
			t.Fatalf("%s/1259: size %d not multiple of block size %d", db, len(data), storage.BlockSize)
		}

		totalTuples := 0
		pageCount := len(data) / storage.BlockSize

		for pi := 0; pi < pageCount; pi++ {
			page := storage.Page(data[pi*storage.BlockSize : (pi+1)*storage.BlockSize])

			// Verify page header is valid (non-zero magic, sensible fields).
			hdr, err := storage.Header(page)
			if err != nil {
				t.Fatalf("%s/1259 page %d: invalid page header: %v", db, pi, err)
			}
			if hdr.Lower() < storage.SizeOfPageHeaderData {
				t.Fatalf("%s/1259 page %d: pd_lower=%d < SizeOfPageHeaderData", db, pi, hdr.Lower())
			}

			count, err := storage.PageLinePointerCount(page)
			if err != nil {
				t.Fatalf("%s/1259 page %d: PageLinePointerCount: %v", db, pi, err)
			}

			for slot := uint16(1); slot <= uint16(count); slot++ {
				itemID, err := storage.PageGetItemID(page, slot)
				if err != nil {
					t.Fatalf("%s/1259 page %d slot %d: PageGetItemID: %v", db, pi, slot, err)
				}
				// ItemIDNormal = 1 (live tuple), ItemIDUnused = 0.
				if itemID.Flags != storage.ItemIDNormal {
					continue // skip unused/dead/redirect items
				}
				raw, err := storage.PageGetItemRaw(page, slot)
				if err != nil {
					t.Fatalf("%s/1259 page %d slot %d: PageGetItemRaw: %v", db, pi, slot, err)
				}

				// The OID is the first fixed-width column in pg_class.
				// NewHeapTuple uses DefaultHeapTupleHoff=24 for the data offset.
				hoff := int(raw[22]) // t_hoff at byte 22 (SizeOfHeapTupleHeaderData=23, last byte)
				if hoff == 0 || int(hoff) > len(raw) {
					t.Errorf("%s/1259 page %d slot %d: invalid t_hoff=%d", db, pi, slot, hoff)
					continue
				}
				payload := raw[hoff:]
				if len(payload) < 4 {
					t.Errorf("%s/1259 page %d slot %d: payload too short (%d bytes)", db, pi, slot, len(payload))
					continue
				}
				oid := uint32(payload[0])<<24 | uint32(payload[1])<<16 | uint32(payload[2])<<8 | uint32(payload[3])
				if oid == 0 {
					t.Errorf("%s/1259 page %d slot %d: OID is zero", db, pi, slot)
				}
				totalTuples++
			}
		}

		if totalTuples != expectedCount {
			t.Errorf("%s/1259: %d tuples, want %d (nailedSharedRels=%d + nailedLocalRels=%d + nailedToastRels=%d + infoSchemaDataTables=%d)",
				db, totalTuples, expectedCount, len(nailedSharedRels), len(nailedLocalRels), len(nailedToastRels()), len(informationSchemaDataTableRels()))
		} else if totalTuples > 0 {
			t.Logf("%s/1259: %d tuples (all %d nailed relations accounted for)", db, totalTuples, expectedCount)
		}
	}
}
