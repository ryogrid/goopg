package initdb

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"testing"
)

// TestWriteRelcacheInitFile_SharedFormat verifies that the shared init file
// (global/pg_internal.init) produced by writeRelcacheInitFile has the correct
// magic number and exactly 5 heap + 6 index records (NUM_CRITICAL_SHARED_RELS=5,
// NUM_CRITICAL_SHARED_INDEXES=6, relcache.c:4086 and :4226).
func TestWriteRelcacheInitFile_SharedFormat(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "global"), 0o755); err != nil {
		t.Fatal(err)
	}

	if err := writeRelcacheInitFile(dir, true, nailedSharedRels); err != nil {
		t.Fatalf("writeRelcacheInitFile(shared): %v", err)
	}

	data, err := os.ReadFile(filepath.Join(dir, "global", "pg_internal.init"))
	if err != nil {
		t.Fatal(err)
	}

	nHeaps, nIndexes, err := simulateLoadRelcacheInitFile(data)
	if err != nil {
		t.Fatalf("reader simulator: %v", err)
	}
	if nHeaps != 5 {
		t.Errorf("shared: got %d heap rels, want 5", nHeaps)
	}
	if nIndexes != 6 {
		t.Errorf("shared: got %d indexes, want 6", nIndexes)
	}
}

// TestWriteRelcacheInitFile_LocalFormat verifies that the local init file
// (base/1/pg_internal.init) has the correct magic number and exactly 4 heap +
// 7 index records (NUM_CRITICAL_LOCAL_RELS=4, NUM_CRITICAL_LOCAL_INDEXES=7,
// relcache.c:4143 and :4194).
func TestWriteRelcacheInitFile_LocalFormat(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "base", "1"), 0o755); err != nil {
		t.Fatal(err)
	}

	if err := writeRelcacheInitFile(dir, false, nailedLocalRels); err != nil {
		t.Fatalf("writeRelcacheInitFile(local): %v", err)
	}

	data, err := os.ReadFile(filepath.Join(dir, "base", "1", "pg_internal.init"))
	if err != nil {
		t.Fatal(err)
	}

	nHeaps, nIndexes, err := simulateLoadRelcacheInitFile(data)
	if err != nil {
		t.Fatalf("reader simulator: %v", err)
	}
	if nHeaps != 4 {
		t.Errorf("local: got %d heap rels, want 4", nHeaps)
	}
	if nIndexes != 7 {
		t.Errorf("local: got %d indexes, want 7", nIndexes)
	}
}

// TestWriteRelcacheInitFile_ComparisonProcs verifies that the comparison
// support function (proc #1 = position 0 in rd_support) is non-zero for
// every critical index column.  A zero OID at position 0 would make
// _bt_mkscankey → index_getprocinfo skip fmgr_info_cxt, leaving fn_addr
// NULL and crashing the first btree scan.
func TestWriteRelcacheInitFile_ComparisonProcs(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	for _, sub := range []string{"global", filepath.Join("base", "1")} {
		if err := os.MkdirAll(filepath.Join(dir, sub), 0o755); err != nil {
			t.Fatal(err)
		}
	}

	for _, shared := range []bool{true, false} {
		rels := nailedSharedRels
		if !shared {
			rels = nailedLocalRels
		}
		if err := writeRelcacheInitFile(dir, shared, rels); err != nil {
			t.Fatalf("writeRelcacheInitFile(shared=%v): %v", shared, err)
		}
	}

	for _, tc := range []struct {
		name  string
		path  string
		idxOIDs []uint32
	}{
		{
			"shared",
			filepath.Join(dir, "global", "pg_internal.init"),
			criticalSharedIndexOIDs,
		},
		{
			"local",
			filepath.Join(dir, "base", "1", "pg_internal.init"),
			criticalLocalIndexOIDs,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			data, err := os.ReadFile(tc.path)
			if err != nil {
				t.Fatal(err)
			}
			procs, err := collectIndexCmpProcs(data)
			if err != nil {
				t.Fatalf("collectIndexCmpProcs: %v", err)
			}
			for _, oid := range tc.idxOIDs {
				proc, ok := procs[oid]
				if !ok {
					t.Errorf("OID %d: not found in init file", oid)
					continue
				}
				if proc == 0 {
					t.Errorf("OID %d: comparison proc is 0 (InvalidOid)", oid)
				}
			}
		})
	}
}

// simulateLoadRelcacheInitFile mimics PG's load_relcache_init_file reader
// (relcache.c:6147-6547).  It validates the magic number, structural
// length fields, and index sub-record format, counting heap rels and
// indexes separately. Returns the counts or the first format error.
func simulateLoadRelcacheInitFile(data []byte) (nHeaps, nIndexes int, err error) {
	r := bytes.NewReader(data)

	var magic uint32
	if err := binary.Read(r, binary.LittleEndian, &magic); err != nil {
		return 0, 0, err
	}
	if magic != relCacheInitFileMagic {
		return 0, 0, fmt.Errorf("bad magic: 0x%x, want 0x%x", magic, relCacheInitFileMagic)
	}

	for {
		// RelationData
		var relDescLen uint32
		if err := binary.Read(r, binary.LittleEndian, &relDescLen); err != nil {
			if err == io.EOF {
				break
			}
			return 0, 0, fmt.Errorf("relDescLen: %w", err)
		}
		if relDescLen != sizeofRelationData {
			return 0, 0, fmt.Errorf("relDescLen %d != %d", relDescLen, sizeofRelationData)
		}
		relData := make([]byte, relDescLen)
		if _, err := io.ReadFull(r, relData); err != nil {
			return 0, 0, fmt.Errorf("RelationData body: %w", err)
		}

		// FormData_pg_class
		var relFormLen uint32
		if err := binary.Read(r, binary.LittleEndian, &relFormLen); err != nil {
			return 0, 0, fmt.Errorf("relFormLen: %w", err)
		}
		if relFormLen != sizeofFormDataPgClass {
			return 0, 0, fmt.Errorf("relFormLen %d != %d", relFormLen, sizeofFormDataPgClass)
		}
		relForm := make([]byte, relFormLen)
		if _, err := io.ReadFull(r, relForm); err != nil {
			return 0, 0, fmt.Errorf("FormData_pg_class body: %w", err)
		}
		// relkind at offset 119, relnatts at offset 120 (uint16 LE)
		relkind := relForm[119]
		relnatts := int(binary.LittleEndian.Uint16(relForm[120:122]))

		// FormData_pg_attribute for each column
		for i := 0; i < relnatts; i++ {
			var attrLen uint32
			if err := binary.Read(r, binary.LittleEndian, &attrLen); err != nil {
				return 0, 0, fmt.Errorf("attrLen col %d: %w", i, err)
			}
			if attrLen != attrFixedPartSize {
				return 0, 0, fmt.Errorf("attrLen %d != %d at col %d", attrLen, attrFixedPartSize, i)
			}
			if _, err := io.ReadFull(r, make([]byte, attrLen)); err != nil {
				return 0, 0, fmt.Errorf("attr body col %d: %w", i, err)
			}
		}

		// reloptions length (zero)
		var optLen uint32
		if err := binary.Read(r, binary.LittleEndian, &optLen); err != nil {
			return 0, 0, fmt.Errorf("optLen: %w", err)
		}
		if optLen > 0 {
			if _, err := io.ReadFull(r, make([]byte, optLen)); err != nil {
				return 0, 0, fmt.Errorf("reloptions body: %w", err)
			}
		}

		if relkind == 'i' {
			nIndexes++
			if err := readIndexSubrecord(r, relnatts); err != nil {
				return 0, 0, fmt.Errorf("index sub-record: %w", err)
			}
		} else {
			nHeaps++
		}
	}

	return nHeaps, nIndexes, nil
}

// readIndexSubrecord reads and validates the index sub-record fields
// (relcache.c:6316-6428). No size checks on arrays beyond existence — PG's
// reader also just reads whatever length was written.
func readIndexSubrecord(r *bytes.Reader, relnatts int) error {
	// pg_index HeapTuple
	var tupLen uint32
	if err := binary.Read(r, binary.LittleEndian, &tupLen); err != nil {
		return fmt.Errorf("indexTupleLen: %w", err)
	}
	if tupLen < uint32(heapTupleDataSize+pgIndexTupleHoff) {
		return fmt.Errorf("indexTupleLen %d too small (min %d)", tupLen, heapTupleDataSize+pgIndexTupleHoff)
	}
	tup := make([]byte, tupLen)
	if _, err := io.ReadFull(r, tup); err != nil {
		return fmt.Errorf("indexTuple body: %w", err)
	}

	// opfamily
	var opfamLen uint32
	if err := binary.Read(r, binary.LittleEndian, &opfamLen); err != nil {
		return fmt.Errorf("opfamilyLen: %w", err)
	}
	if opfamLen != uint32(relnatts*4) {
		return fmt.Errorf("opfamilyLen %d != relnatts(%d)*4", opfamLen, relnatts)
	}
	if _, err := io.ReadFull(r, make([]byte, opfamLen)); err != nil {
		return fmt.Errorf("opfamily body: %w", err)
	}

	// opcintype
	var opcintypeLen uint32
	if err := binary.Read(r, binary.LittleEndian, &opcintypeLen); err != nil {
		return fmt.Errorf("opcintypeLen: %w", err)
	}
	if opcintypeLen != uint32(relnatts*4) {
		return fmt.Errorf("opcintypeLen %d != relnatts(%d)*4", opcintypeLen, relnatts)
	}
	if _, err := io.ReadFull(r, make([]byte, opcintypeLen)); err != nil {
		return fmt.Errorf("opcintype body: %w", err)
	}

	// support (relnatts * btreeAmsupport entries)
	var supportLen uint32
	if err := binary.Read(r, binary.LittleEndian, &supportLen); err != nil {
		return fmt.Errorf("supportLen: %w", err)
	}
	wantSupport := uint32(relnatts * btreeAmsupport * 4)
	if supportLen != wantSupport {
		return fmt.Errorf("supportLen %d != relnatts(%d)*amsupport(%d)*4=%d",
			supportLen, relnatts, btreeAmsupport, wantSupport)
	}
	support := make([]byte, supportLen)
	if _, err := io.ReadFull(r, support); err != nil {
		return fmt.Errorf("support body: %w", err)
	}

	// indcollation
	var indcollLen uint32
	if err := binary.Read(r, binary.LittleEndian, &indcollLen); err != nil {
		return fmt.Errorf("indcollLen: %w", err)
	}
	if _, err := io.ReadFull(r, make([]byte, indcollLen)); err != nil {
		return fmt.Errorf("indcollation body: %w", err)
	}

	// indoption
	var indoptLen uint32
	if err := binary.Read(r, binary.LittleEndian, &indoptLen); err != nil {
		return fmt.Errorf("indoptLen: %w", err)
	}
	if _, err := io.ReadFull(r, make([]byte, indoptLen)); err != nil {
		return fmt.Errorf("indoption body: %w", err)
	}

	// opcoptions (one uint32 per column, zero for standard btree indexes)
	for i := 0; i < relnatts; i++ {
		var opcoptLen uint32
		if err := binary.Read(r, binary.LittleEndian, &opcoptLen); err != nil {
			return fmt.Errorf("opcoptLen col %d: %w", i, err)
		}
		if opcoptLen > 0 {
			if _, err := io.ReadFull(r, make([]byte, opcoptLen)); err != nil {
				return fmt.Errorf("opcoptions body col %d: %w", i, err)
			}
		}
	}
	return nil
}

// collectIndexCmpProcs parses an init file and returns a map from the
// indexrelid (extracted from the pg_index tuple) to the comparison proc OID
// (rd_support[0] = support proc #1). Used to verify btoidcmp / btnamecmp etc.
// are wired correctly.
func collectIndexCmpProcs(data []byte) (map[uint32]uint32, error) {
	r := bytes.NewReader(data)
	out := make(map[uint32]uint32)

	var magic uint32
	if err := binary.Read(r, binary.LittleEndian, &magic); err != nil {
		return nil, err
	}
	if magic != relCacheInitFileMagic {
		return nil, fmt.Errorf("bad magic")
	}

	for {
		// RelationData (rd_id at offset 0)
		var relDescLen uint32
		if err := binary.Read(r, binary.LittleEndian, &relDescLen); err != nil {
			if err == io.EOF {
				break
			}
			return nil, err
		}
		relData := make([]byte, relDescLen)
		if _, err := io.ReadFull(r, relData); err != nil {
			return nil, err
		}
		relOID := binary.LittleEndian.Uint32(relData[0:4])

		// FormData_pg_class
		var relFormLen uint32
		if err := binary.Read(r, binary.LittleEndian, &relFormLen); err != nil {
			return nil, err
		}
		relForm := make([]byte, relFormLen)
		if _, err := io.ReadFull(r, relForm); err != nil {
			return nil, err
		}
		relkind := relForm[119]
		relnatts := int(binary.LittleEndian.Uint16(relForm[120:122]))

		// Attributes
		for i := 0; i < relnatts; i++ {
			var attrLen uint32
			if err := binary.Read(r, binary.LittleEndian, &attrLen); err != nil {
				return nil, err
			}
			if _, err := io.ReadFull(r, make([]byte, attrLen)); err != nil {
				return nil, err
			}
		}
		// Options
		var optLen uint32
		if err := binary.Read(r, binary.LittleEndian, &optLen); err != nil {
			return nil, err
		}
		if optLen > 0 {
			if _, err := io.ReadFull(r, make([]byte, optLen)); err != nil {
				return nil, err
			}
		}

		if relkind != 'i' {
			continue
		}

		// pg_index tuple — read the indexrelid from the user data
		var tupLen uint32
		if err := binary.Read(r, binary.LittleEndian, &tupLen); err != nil {
			return nil, err
		}
		tup := make([]byte, tupLen)
		if _, err := io.ReadFull(r, tup); err != nil {
			return nil, err
		}
		// t_data starts at heapTupleDataSize; user data at heapTupleDataSize+pgIndexTupleHoff
		// indexrelid is the first uint32 in user data
		userOff := heapTupleDataSize + pgIndexTupleHoff
		if int(userOff+4) > len(tup) {
			return nil, fmt.Errorf("tuple too short for indexrelid")
		}
		indexRelid := binary.LittleEndian.Uint32(tup[userOff : userOff+4])
		_ = indexRelid // we use relOID as the key (same thing for these indexes)

		// opfamily
		var opfamLen uint32
		if err := binary.Read(r, binary.LittleEndian, &opfamLen); err != nil {
			return nil, err
		}
		if _, err := io.ReadFull(r, make([]byte, opfamLen)); err != nil {
			return nil, err
		}
		// opcintype
		var opcintypeLen uint32
		if err := binary.Read(r, binary.LittleEndian, &opcintypeLen); err != nil {
			return nil, err
		}
		if _, err := io.ReadFull(r, make([]byte, opcintypeLen)); err != nil {
			return nil, err
		}
		// support: first uint32 is the comparison proc for col 0
		var supportLen uint32
		if err := binary.Read(r, binary.LittleEndian, &supportLen); err != nil {
			return nil, err
		}
		support := make([]byte, supportLen)
		if _, err := io.ReadFull(r, support); err != nil {
			return nil, err
		}
		var cmpProc uint32
		if len(support) >= 4 {
			cmpProc = binary.LittleEndian.Uint32(support[0:4])
		}
		out[relOID] = cmpProc

		// indcollation
		var indcollLen uint32
		if err := binary.Read(r, binary.LittleEndian, &indcollLen); err != nil {
			return nil, err
		}
		if _, err := io.ReadFull(r, make([]byte, indcollLen)); err != nil {
			return nil, err
		}
		// indoption
		var indoptLen uint32
		if err := binary.Read(r, binary.LittleEndian, &indoptLen); err != nil {
			return nil, err
		}
		if _, err := io.ReadFull(r, make([]byte, indoptLen)); err != nil {
			return nil, err
		}
		// opcoptions
		for i := 0; i < relnatts; i++ {
			var opcoptLen uint32
			if err := binary.Read(r, binary.LittleEndian, &opcoptLen); err != nil {
				return nil, err
			}
			if opcoptLen > 0 {
				if _, err := io.ReadFull(r, make([]byte, opcoptLen)); err != nil {
					return nil, err
				}
			}
		}
	}
	return out, nil
}
