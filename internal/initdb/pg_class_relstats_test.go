package initdb

import (
	"encoding/binary"
	"math"
	"os"
	"path/filepath"
	"strconv"
	"testing"
)

// TestStampPgClassRelStats pins M0149-0001g slice 25: every bootstrapped
// pg_class row carries relpages equal to its relation file's block count, and
// heaps a reltuples equal to their live line pointers (PG's initdb ends with
// VACUUM FREEZE, so a PG node plans catalog queries from measured sizes).
func TestStampPgClassRelStats(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "data")
	if err := Init(Options{DataDir: dir, NoSync: true}); err != nil {
		t.Fatalf("Init: %v", err)
	}
	le := binary.LittleEndian
	data, err := os.ReadFile(filepath.Join(dir, "base", "5", "1259"))
	if err != nil {
		t.Fatal(err)
	}
	seen := map[uint32]bool{}
	for pg := 0; pg+relStatsBlockSize <= len(data); pg += relStatsBlockSize {
		page := data[pg : pg+relStatsBlockSize]
		lower := int(le.Uint16(page[12:14]))
		for slot := 24; slot+4 <= lower; slot += 4 {
			lp := le.Uint32(page[slot:])
			if (lp>>15)&3 != 1 {
				continue
			}
			off := int(lp & 0x7FFF)
			d := page[off+int(page[off+22]):]
			oid := le.Uint32(d[pgClassOffOID:])
			kind := d[pgClassOffRelkind]
			pages := int32(le.Uint32(d[pgClassOffRelpages:]))
			tuples := math.Float32frombits(le.Uint32(d[pgClassOffReltuples:]))
			fileDir := filepath.Join(dir, "base", "5")
			if le.Uint32(d[pgClassOffReltablespace:]) == 1664 {
				fileDir = filepath.Join(dir, "global")
			}
			node := le.Uint32(d[pgClassOffRelfilenode:])
			if node == 0 {
				node = oid
			}
			switch kind {
			case 'r', 'i', 't':
				fi, err := os.Stat(filepath.Join(fileDir, strconv.FormatUint(uint64(node), 10)))
				want := int32(0)
				if err == nil {
					want = int32(fi.Size() / relStatsBlockSize)
				}
				if pages != want {
					t.Errorf("oid %d relkind %c relpages=%d, file has %d blocks", oid, kind, pages, want)
				}
			case 'v':
				if tuples != -1 {
					t.Errorf("view %d reltuples=%v, want -1", oid, tuples)
				}
			}
			seen[oid] = true
			if oid == 1255 && int(tuples) != len(pgProcInitialEntries()) {
				t.Errorf("pg_proc reltuples=%v, want %d rows", tuples, len(pgProcInitialEntries()))
			}
		}
	}
	for _, oid := range []uint32{1255, 1259, 2662, 3394} {
		if !seen[oid] {
			t.Errorf("pg_class row %d not found", oid)
		}
	}
}
