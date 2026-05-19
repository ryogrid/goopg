package testport

// TestE2E_CreateTablePersistsRelnameIndexEntryOnDisk diagnoses M0106-0010
// batched-40: after `CREATE TABLE public.bench_log` runs on a goopg primary
// and the primary stops cleanly (forcing a shutdown checkpoint), the on-disk
// pg_class_relname_nsp_index (OID 2663) under `base/5/` must carry an
// IndexTuple whose NameData column reads "bench_log".
//
// This is the most direct check of whether the M0106-0010 runtime sys-btree
// insert path (sys_catalog_index_insert.go + sys_catalog_btree_split.go)
// actually persists user-table catalog entries through to disk — independent
// of any PG18 standby reading. If this test fails, the bug is in the
// goopg-primary executor or storage layer. If it passes but PG18 still cannot
// resolve `public.bench_log`, the bug is in the on-disk format or the WAL
// record path used during replay.

import (
	"encoding/binary"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/goopg/goopg/internal/storage"
	"github.com/goopg/goopg/internal/testutil/cluster"
)

const m0106BatchTestPgClassRelnameNspIndexOID = 2663

func TestE2E_CreateTablePersistsRelnameIndexEntryOnDisk(t *testing.T) {
	if os.Getenv("GOOPG_RUN_BLOCKED_M0102_E2E") == "" {
		t.Skip("set GOOPG_RUN_BLOCKED_M0102_E2E=1 to run this disk-level diagnostic")
	}

	repo := repoRoot(t)
	dataDir := filepath.Join(t.TempDir(), "goopg-primary")
	c, err := cluster.New("m0106-batched-40-disk", cluster.Options{
		RepoRoot:     repo,
		DataDir:      dataDir,
		StartupWait:  45 * time.Second,
		ShutdownWait: 10 * time.Second,
	})
	if err != nil {
		t.Fatalf("cluster.New: %v", err)
	}
	if err := c.Init(); err != nil {
		t.Fatalf("Init: %v", err)
	}

	// Inspect the BOOTSTRAP-only state of base/5/2663 (before CREATE TABLE).
	// This isolates how many entries the bootstrap puts there vs. what
	// runtime CREATE TABLE adds.
	relfileEarly := filepath.Join(dataDir, "base", "5", formatOidPath(m0106BatchTestPgClassRelnameNspIndexOID))
	if data, err := os.ReadFile(relfileEarly); err == nil {
		t.Logf("=== POST-INIT (pre-CREATE TABLE) layout ===")
		t.Logf("file size = %d bytes (= %d pages)", len(data), len(data)/storage.BlockSize)
		dumpRelnameNspIndexLayout(t, data)
	} else {
		t.Logf("post-init inspection skipped: %v", err)
	}

	if err := c.Start(); err != nil {
		t.Fatalf("Start: %v", err)
	}
	if err := runSQLSimple(t, c, "CREATE TABLE public.bench_log (client int NOT NULL, src text NOT NULL)"); err != nil {
		_ = c.Stop(cluster.ShutdownImmediate)
		t.Fatalf("CREATE TABLE: %v", err)
	}
	// Clean stop triggers shutdown checkpoint, flushing dirty buffers.
	if err := c.Stop(cluster.ShutdownFast); err != nil {
		t.Fatalf("Stop: %v", err)
	}

	// Also dump base/1/2663 for comparison — runtime inserts use
	// catalog.DefaultDBOid=1, so this file is where executor writes land.
	relfileOne := filepath.Join(dataDir, "base", "1", formatOidPath(m0106BatchTestPgClassRelnameNspIndexOID))
	if data, err := os.ReadFile(relfileOne); err == nil {
		t.Logf("=== POST-CREATE-TABLE base/1/2663 layout ===")
		t.Logf("file size = %d bytes (= %d pages)", len(data), len(data)/storage.BlockSize)
		dumpRelnameNspIndexLayout(t, data)
		ok, _ := scanRelnameNspIndexForName(data, "bench_log")
		t.Logf("bench_log present in base/1/2663: %v", ok)
	}

	// Inspect base/5/2663 directly on disk. The bootstrap-built leaf-root at
	// block 1 holds ~29 nailed-rel entries; the runtime insert of bench_log
	// should land somewhere in block 1 (no split needed for so few entries).
	relfile := filepath.Join(dataDir, "base", "5",
		formatOidPath(m0106BatchTestPgClassRelnameNspIndexOID))
	data, err := os.ReadFile(relfile)
	if err != nil {
		t.Fatalf("read %s: %v", relfile, err)
	}
	if len(data) < 2*storage.BlockSize {
		t.Fatalf("relfile too small: %d bytes (want >= 2 * %d)", len(data), storage.BlockSize)
	}

	t.Logf("file size = %d bytes (= %d pages)", len(data), len(data)/storage.BlockSize)
	dumpRelnameNspIndexLayout(t, data)
	found, totalEntries := scanRelnameNspIndexForName(data, "bench_log")
	if !found {
		t.Fatalf("bench_log NOT found in base/5/2663 after CREATE TABLE; total IndexTuples across all leaves = %d. "+
			"This means the M0106-0010 runtime sys-btree insert path either did not run, did not "+
			"persist the dirty page, or wrote bytes the scanner could not parse.", totalEntries)
	}
	t.Logf("bench_log found on disk in base/5/2663 (total IndexTuples = %d)", totalEntries)
}

// formatOidPath converts a pg_class OID to its `relfilenode` filename. For
// nailed system relations the filenode equals the OID.
func formatOidPath(oid uint32) string {
	return uintToDecimal(uint64(oid))
}

func uintToDecimal(n uint64) string {
	if n == 0 {
		return "0"
	}
	var buf [20]byte
	i := len(buf)
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	return string(buf[i:])
}

// scanRelnameNspIndexForName walks every 8 KiB page in `data` looking for an
// IndexTuple with NameData column matching `target`. Returns (found, total).
// IndexTuple layout for pg_class_relname_nsp_index:
//
//	bytes 0..7:   IndexTupleHeader (block, offset, t_info)
//	bytes 8..71:  NameData (64-byte NUL-padded name)
//	bytes 72..75: nsoid (uint32 LE)
//	bytes 76..79: MAXALIGN padding
//
// We skip pages whose opaque flag is not BTP_LEAF (level=0). Pivot/high-key
// tuples on non-rightmost leaves carry a truncated key at slot 1; for our
// purpose we still scan their NameData since the 8..72 window is intact.
func scanRelnameNspIndexForName(data []byte, target string) (bool, int) {
	const tupleSize = 80
	const nameOff = 8
	const nameLen = 64
	pages := len(data) / storage.BlockSize
	totalTuples := 0
	for p := 0; p < pages; p++ {
		page := data[p*storage.BlockSize : (p+1)*storage.BlockSize]
		// Reject obviously empty pages.
		if isAllZero(page) {
			continue
		}
		// Read btpo_flags from the opaque trailer at end of page.
		off := storage.BlockSize - 16
		flags := binary.LittleEndian.Uint16(page[off+12 : off+14])
		const btpLeaf = uint16(0x01)
		const btpMeta = uint16(0x08)
		if flags&btpMeta != 0 {
			continue
		}
		if flags&btpLeaf == 0 {
			// Internal page; skip downlinks.
			continue
		}
		count, err := storage.PageLinePointerCount(page)
		if err != nil {
			continue
		}
		for slot := 1; slot <= count; slot++ {
			raw, err := storage.PageGetItemRaw(page, uint16(slot))
			if err != nil || len(raw) < nameOff+nameLen {
				continue
			}
			totalTuples++
			nameBytes := raw[nameOff : nameOff+nameLen]
			// Trim NUL padding.
			end := nameLen
			for i, c := range nameBytes {
				if c == 0 {
					end = i
					break
				}
			}
			name := string(nameBytes[:end])
			if name == target && len(raw) >= tupleSize {
				return true, totalTuples
			}
		}
	}
	return false, totalTuples
}

func dumpRelnameNspIndexLayout(t *testing.T, data []byte) {
	t.Helper()
	const nameOff = 8
	const nameLen = 64
	pages := len(data) / storage.BlockSize
	for p := 0; p < pages; p++ {
		page := data[p*storage.BlockSize : (p+1)*storage.BlockSize]
		if isAllZero(page) {
			t.Logf("page %d: ALL ZERO", p)
			continue
		}
		off := storage.BlockSize - 16
		prev := binary.LittleEndian.Uint32(page[off+0 : off+4])
		next := binary.LittleEndian.Uint32(page[off+4 : off+8])
		level := binary.LittleEndian.Uint32(page[off+8 : off+12])
		flags := binary.LittleEndian.Uint16(page[off+12 : off+14])
		count, _ := storage.PageLinePointerCount(page)
		const btpLeaf = uint16(0x01)
		const btpMeta = uint16(0x08)
		const btpRoot = uint16(0x02)
		kind := "?"
		switch {
		case flags&btpMeta != 0:
			kind = "META"
			// Decode metapage btm_root/btm_level
			base := storage.SizeOfPageHeaderData
			btmRoot := binary.LittleEndian.Uint32(page[base+8 : base+12])
			btmLevel := binary.LittleEndian.Uint32(page[base+12 : base+16])
			t.Logf("page %d: %s flags=%#x lpc=%d btm_root=%d btm_level=%d", p, kind, flags, count, btmRoot, btmLevel)
			continue
		case flags&btpLeaf != 0:
			kind = "LEAF"
			if flags&btpRoot != 0 {
				kind = "LEAFROOT"
			}
		case flags&btpRoot != 0:
			kind = "INTROOT"
		default:
			kind = "INTERNAL"
		}
		// Read first 3 keys' names to characterize the page.
		var firstNames []string
		for slot := 1; slot <= count && slot <= 3; slot++ {
			raw, err := storage.PageGetItemRaw(page, uint16(slot))
			if err != nil || len(raw) < nameOff+nameLen {
				continue
			}
			nb := raw[nameOff : nameOff+nameLen]
			end := nameLen
			for i, c := range nb {
				if c == 0 {
					end = i
					break
				}
			}
			firstNames = append(firstNames, string(nb[:end]))
		}
		// Read last key's name too.
		var lastName string
		if count > 0 {
			raw, err := storage.PageGetItemRaw(page, uint16(count))
			if err == nil && len(raw) >= nameOff+nameLen {
				nb := raw[nameOff : nameOff+nameLen]
				end := nameLen
				for i, c := range nb {
					if c == 0 {
						end = i
						break
					}
				}
				lastName = string(nb[:end])
			}
		}
		t.Logf("page %d: %s flags=%#x lpc=%d prev=%d next=%d level=%d firsts=%v last=%q",
			p, kind, flags, count, prev, next, level, firstNames, lastName)
	}
}

func isAllZero(b []byte) bool {
	for _, x := range b {
		if x != 0 {
			return false
		}
	}
	return true
}
