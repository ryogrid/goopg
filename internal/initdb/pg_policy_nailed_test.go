package initdb

import (
	"bytes"
	"encoding/binary"
	"os"
	"path/filepath"
	"testing"

	"github.com/goopg/goopg/internal/storage"
)

// M0131-S9.3e: pg_policy (3256) is the first catalog goopg bootstraps because
// a hosted PG needs it to evaluate a VIEW — `pg_policies` (12018) opens it in
// its FROM list, and without a pg_class row PG fails the view with
// `could not open relation with OID 3256` (ceiling #4 of the S9 corpus).
//
// These guards pin the two halves that must agree for that to keep working:
// the transcription of `postgres/src/include/catalog/pg_policy.h`, and the
// bytes a hosted PG actually reads out of base/{1,5}. Asserting pgPolicyAttrs()
// alone would not do it — the value that matters is on disk, and the same
// "green in Go, absent on disk" gap is what M0131-S6 and S14.1 both cost.

// TestPgPolicyAttrsMatchUpstreamHeader pins the column table against PG18's
// CATALOG(pg_policy,3256,PolicyRelationId) declaration, column for column.
// polroles is BKI_FORCE_NOT_NULL (zero means PUBLIC, never NULL); the two
// pg_node_tree quals are nullable because a policy with no USING / WITH CHECK
// clause stores nothing there.
func TestPgPolicyAttrsMatchUpstreamHeader(t *testing.T) {
	want := []nailedAttr{
		{Name: "oid", TypeOID: 26, Num: 1, Len: 4, NotNull: true},
		{Name: "polname", TypeOID: 19, Num: 2, Len: 64, NotNull: true},
		{Name: "polrelid", TypeOID: 26, Num: 3, Len: 4, NotNull: true},
		{Name: "polcmd", TypeOID: 18, Num: 4, Len: 1, NotNull: true},
		{Name: "polpermissive", TypeOID: 16, Num: 5, Len: 1, NotNull: true},
		{Name: "polroles", TypeOID: 1028, Num: 6, Len: -1, NotNull: true},
		{Name: "polqual", TypeOID: 194, Num: 7, Len: -1, NotNull: false},
		{Name: "polwithcheck", TypeOID: 194, Num: 8, Len: -1, NotNull: false},
	}
	got := pgPolicyAttrs()
	if len(got) != len(want) {
		t.Fatalf("pgPolicyAttrs() has %d columns, upstream pg_policy.h has %d", len(got), len(want))
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("pgPolicyAttrs()[%d] = %+v, want %+v", i, got[i], want[i])
		}
	}

	// The nailedRel row must agree with the column table, and pg_policy must
	// be listed as a LOCAL (per-database) catalog — it is not shared.
	var found *nailedRel
	for i := range nailedLocalRels {
		if nailedLocalRels[i].OID == 3256 {
			found = &nailedLocalRels[i]
			break
		}
	}
	if found == nil {
		t.Fatal("pg_policy (3256) is not in nailedLocalRels — a hosted PG cannot open " +
			"pg_policies, which fails with `could not open relation with OID 3256`")
	}
	if found.RelName != "pg_policy" || found.RelKind != 'r' || found.IsShared {
		t.Errorf("nailedRel 3256 = {%q, kind %q, shared %v}, want {\"pg_policy\", 'r', false}",
			found.RelName, found.RelKind, found.IsShared)
	}
	if int(found.RelNatts) != len(want) {
		t.Errorf("nailedRel 3256 RelNatts = %d, want %d — a short relnatts truncates the "+
			"trailing columns when PG copies rd_rel out of goopg's pg_class tuple (M0131-S14.1)",
			found.RelNatts, len(want))
	}
}

// TestPgPolicyIsOnDiskAfterInit reads what a hosted PG reads: the pg_class
// tuple for 3256, its eight pg_attribute rows, and the empty heap file the
// view's seq scan opens. All three in BOTH base/1 and base/5 — every sibling
// catalog is written to both, and the S15 template0 refresh clones base/5.
func TestPgPolicyIsOnDiskAfterInit(t *testing.T) {
	dir := t.TempDir()
	if err := Init(Options{DataDir: dir, NoSync: true}); err != nil {
		t.Fatal(err)
	}

	// FormData_pg_class fixed-width offsets, per pgClassColDefs() (same
	// constants TestBootstrappedViewsCarryRelhasrules reads).
	const (
		offRelkind  = 119
		offRelnatts = 120 // int2, immediately behind relkind; relchecks 122, relhasrules 124
	)

	for _, db := range []string{"base/1", "base/5"} {
		// 1. the heap file the seq scan opens: one empty, initialised page.
		heap := filepath.Join(dir, db, "3256")
		info, err := os.Stat(heap)
		if err != nil {
			t.Fatalf("%s/3256: %v — pg_policy needs a physical file even though "+
				"goopg never writes a row into it (3256 belongs in "+
				"mappedLocalCatalogPlaceholderOIDs)", db, err)
		}
		if info.Size() != int64(storage.BlockSize) {
			t.Errorf("%s/3256 is %d bytes, want exactly one %d-byte page",
				db, info.Size(), storage.BlockSize)
		}
		page, err := os.ReadFile(heap)
		if err != nil {
			t.Fatal(err)
		}
		if n, err := storage.PageLinePointerCount(storage.Page(page)); err != nil {
			t.Errorf("%s/3256 is not an initialised page: %v", db, err)
		} else if n != 0 {
			t.Errorf("%s/3256 holds %d line pointers, want 0 — goopg has no on-disk "+
				"CREATE POLICY path, so any row here is a surprise", db, n)
		}

		// 2. the pg_class tuple, found by scanning for relnatts/relkind at a
		//    known payload offset is not enough — locate it by OID, which for
		//    pg_class is the first fixed-width column of the payload.
		classRows := scanHeapPayloadsForOID(t, filepath.Join(dir, db, "1259"), 3256)
		if len(classRows) != 1 {
			t.Fatalf("%s/1259 holds %d pg_class rows for OID 3256, want exactly 1",
				db, len(classRows))
		}
		row := classRows[0]
		if got := int16(binary.LittleEndian.Uint16(row[offRelnatts:])); got != 8 {
			t.Errorf("%s/1259 pg_class(3256).relnatts = %d, want 8", db, got)
		}
		if row[offRelkind] != 'r' {
			t.Errorf("%s/1259 pg_class(3256).relkind = %q, want 'r'", db, row[offRelkind])
		}

		// 3. the eight pg_attribute rows. attrelid is the leading oid column
		//    and attname the 64-byte NameData right behind it (pgAttrColDefs).
		attrRows := scanHeapPayloadsForOID(t, filepath.Join(dir, db, "1249"), 3256)
		if len(attrRows) != 8 {
			t.Fatalf("%s/1249 holds %d pg_attribute rows for attrelid 3256, want 8",
				db, len(attrRows))
		}
		for i, want := range pgPolicyAttrs() {
			name := string(bytes.TrimRight(attrRows[i][4:4+64], "\x00"))
			if name != want.Name {
				t.Errorf("%s/1249 pg_attribute(3256) row %d attname = %q, want %q",
					db, i, name, want.Name)
			}
		}
	}
}

// scanHeapPayloadsForOID returns the tuple payloads (t_hoff applied) of every
// live tuple in a bootstrapped catalog heap whose leading 4-byte column equals
// oid, in on-disk order. Both catalogs this test reads — pg_class and
// pg_attribute — lead with such a column (oid and attrelid respectively).
func scanHeapPayloadsForOID(t *testing.T, path string, oid uint32) [][]byte {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("%s: %v", path, err)
	}
	var out [][]byte
	for pi := 0; pi < len(data)/storage.BlockSize; pi++ {
		page := storage.Page(data[pi*storage.BlockSize : (pi+1)*storage.BlockSize])
		count, err := storage.PageLinePointerCount(page)
		if err != nil {
			t.Fatalf("%s page %d: %v", path, pi, err)
		}
		for slot := uint16(1); slot <= uint16(count); slot++ {
			itemID, err := storage.PageGetItemID(page, slot)
			if err != nil {
				t.Fatalf("%s page %d slot %d: %v", path, pi, slot, err)
			}
			if itemID.Flags != storage.ItemIDNormal {
				continue
			}
			raw, err := storage.PageGetItemRaw(page, slot)
			if err != nil {
				t.Fatalf("%s page %d slot %d: %v", path, pi, slot, err)
			}
			payload := raw[int(raw[22]):]
			if len(payload) < 4 {
				continue
			}
			if binary.LittleEndian.Uint32(payload) == oid {
				out = append(out, payload)
			}
		}
	}
	return out
}
