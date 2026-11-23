package testport

import (
	"encoding/binary"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"testing"

	"github.com/goopg/goopg/internal/testutil/tapnode"
)

// TestPort_Recovery039EndOfWal ports
// postgres/src/test/recovery/t/039_end_of_wal.pl (M0149-0076): hand-crafted
// defective record and page headers are written into the WAL after a crash,
// and crash recovery must stop at them with xlogreader.c's exact
// end-of-WAL messages. Replaces the old shortcut that only checked segment
// file names.
func TestPort_Recovery039EndOfWal(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping recovery TAP port in short mode")
	}
	repo := repoRoot(t)
	tapnode.Available(t, repo)

	const xlpPageMagic = 0xD118        // XLOG_PAGE_MAGIC (access/xlog_internal.h, PG 18)
	const xlpFirstIsContrecord = 0x0001 // XLP_FIRST_IS_CONTRECORD

	node := tapnode.NewGoopg(t, repo, "node")
	node.AppendConf("wal_level = minimal")
	node.AppendConf("autovacuum = off")
	node.AppendConf("checkpoint_timeout = '30min'")
	node.Start()

	q := func(sql string) string { return node.SafePSQL("postgres", sql) }
	getInt := func(name string) int64 {
		v, err := strconv.ParseInt(q("SELECT setting FROM pg_settings WHERE name = '"+name+"'"), 10, 64)
		if err != nil {
			t.Fatalf("setting %s: %v", name, err)
		}
		return v
	}

	// Create a table so a checkpoint is needed at shutdown.
	q("CREATE TABLE t AS SELECT 42")
	walSegmentSize := getInt("wal_segment_size")
	walBlockSize := getInt("wal_block_size")
	tli, err := strconv.ParseInt(q("SELECT timeline_id FROM pg_control_checkpoint();"), 10, 64)
	if err != nil {
		t.Fatalf("timeline: %v", err)
	}
	// Get fresh WAL segment.
	q("SELECT pg_switch_wal();")

	startOfPage := func(lsn int64) int64 { return lsn &^ (walBlockSize - 1) }
	startOfNextPage := func(lsn int64) int64 { return startOfPage(lsn) + walBlockSize }
	emitWal := func(size int64) int64 {
		v, err := strconv.ParseInt(q(fmt.Sprintf("SELECT pg_logical_emit_message(true, '', repeat('a', %d)) - '0/0'", size)), 10, 64)
		if err != nil {
			t.Fatalf("emit_wal: %v", err)
		}
		return v
	}
	insertLSN := func() int64 {
		v, err := strconv.ParseInt(q("SELECT pg_current_wal_insert_lsn() - '0/0'"), 10, 64)
		if err != nil {
			t.Fatalf("insert lsn: %v", err)
		}
		return v
	}
	// Cluster.pm advance_wal_out_of_record_splitting_zone.
	advanceOut := func() int64 {
		threshold := walBlockSize / 4
		end := insertLSN()
		for end%walBlockSize >= walBlockSize-threshold {
			emitWal(threshold)
			end = insertLSN()
		}
		return end
	}
	// Cluster.pm advance_wal_to_record_splitting_zone.
	advanceTo := func() int64 {
		const recordHeaderSize = 24
		end := insertLSN()
		off := end % walBlockSize
		for off <= walBlockSize-512 {
			emitWal(walBlockSize - off - 256)
			end = insertLSN()
			off = end % walBlockSize
		}
		msg := walBlockSize - 80
		for off <= walBlockSize-recordHeaderSize {
			emitWal(msg)
			end = insertLSN()
			old := off
			off = end % walBlockSize
			delta := off - old
			if delta > 8 {
				msg -= 8
			} else if delta <= 0 {
				msg += 8
			}
		}
		return end
	}
	writeWal := func(lsn int64, data []byte) {
		seg := lsn / walSegmentSize
		off := lsn % walSegmentSize
		path := filepath.Join(node.DataDir(), "pg_wal", fmt.Sprintf("%08X%08X%08X", tli, 0, seg))
		f, err := os.OpenFile(path, os.O_RDWR, 0)
		if err != nil {
			t.Fatalf("could not open WAL segment %s: %v", path, err)
		}
		defer f.Close()
		if _, err := f.WriteAt(data, off); err != nil {
			t.Fatalf("write WAL segment %s: %v", path, err)
		}
	}
	// pack("IIIICCBBI", xl_tot_len, xl_xid, xl_prev lo, xl_prev hi, xl_info,
	// xl_rmid, 0, 0, xl_crc) on a little-endian host.
	recordHeader := func(totLen, xid uint32, prev uint64) []byte {
		b := make([]byte, 24)
		binary.LittleEndian.PutUint32(b[0:], totLen)
		binary.LittleEndian.PutUint32(b[4:], xid)
		binary.LittleEndian.PutUint64(b[8:], prev)
		return b
	}
	// pack("SSIIII", xlp_magic, xlp_info, xlp_tli, xlp_pageaddr lo/hi, xlp_rem_len).
	pageHeader := func(magic, info uint16, tli uint32, pageaddr uint64, remLen uint32) []byte {
		b := make([]byte, 20)
		binary.LittleEndian.PutUint16(b[0:], magic)
		binary.LittleEndian.PutUint16(b[2:], info)
		binary.LittleEndian.PutUint32(b[4:], tli)
		binary.LittleEndian.PutUint64(b[8:], pageaddr)
		binary.LittleEndian.PutUint32(b[16:], remLen)
		return b
	}
	logSize := func() int64 {
		st, err := os.Stat(node.LogPath())
		if err != nil {
			return 0
		}
		return st.Size()
	}
	logContains := func(pattern string, offset int64) bool {
		data, err := os.ReadFile(node.LogPath())
		if err != nil || int64(len(data)) < offset {
			return false
		}
		return regexp.MustCompile(pattern).Match(data[offset:])
	}
	ok := func(cond bool, name string) {
		t.Helper()
		if !cond {
			t.Errorf("not ok: %s", name)
		}
	}
	restartAndCheck := func(pattern, name string, corrupt func()) {
		t.Helper()
		node.Stop(tapnode.StopImmediate)
		if corrupt != nil {
			corrupt()
		}
		size := logSize()
		node.Start()
		ok(logContains(pattern, size), name)
	}
	const big = 2 * 1024 * 1024 * 1024

	// Single-page end-of-WAL detection.
	emitWal(0)
	advanceOut()
	restartAndCheck("invalid record length at .*: expected at least 24, got 0", "xl_tot_len zero", nil)

	emitWal(0)
	end := advanceOut()
	restartAndCheck("invalid record length at .*: expected at least 24, got 23", "xl_tot_len short",
		func() { writeWal(end, recordHeader(23, 0, 0)) })

	emitWal(0)
	end = advanceTo()
	restartAndCheck("invalid record length at .*: expected at least 24, got 1", "xl_tot_len short at end-of-page",
		func() { writeWal(end, recordHeader(1, 0, 0)) })

	emitWal(0)
	end = advanceOut()
	restartAndCheck("record with incorrect prev-link 0/DEADBEEF at .*", "xl_prev bad",
		func() { writeWal(end, recordHeader(big, 0, 0xdeadbeef)) })

	emitWal(0)
	advanceOut()
	end = emitWal(10)
	restartAndCheck("incorrect resource manager data checksum in record at .*", "xl_crc bad",
		func() { writeWal(end-8, []byte("!")) })

	// Multi-page end-of-WAL detection, header is not split.
	emitWal(0)
	prev := advanceOut()
	end = emitWal(0)
	restartAndCheck("invalid magic number 0000 .* LSN .*", "xlp_magic zero",
		func() { writeWal(end, recordHeader(big, 0, uint64(prev))) })

	emitWal(0)
	prev = advanceOut()
	end = emitWal(0)
	restartAndCheck("invalid magic number CAFE .* LSN .*", "xlp_magic bad", func() {
		writeWal(end, recordHeader(big, 0, uint64(prev)))
		writeWal(startOfNextPage(end), pageHeader(0xcafe, 0, 1, 0, 0))
	})

	emitWal(0)
	prev = advanceOut()
	end = emitWal(0)
	restartAndCheck("unexpected pageaddr 0/BAAAAAAD in .*, LSN .*,", "xlp_pageaddr bad", func() {
		writeWal(end, recordHeader(big, 0, uint64(prev)))
		writeWal(startOfNextPage(end), pageHeader(xlpPageMagic, 0, 1, 0xbaaaaaad, 0))
	})

	emitWal(0)
	prev = advanceOut()
	end = emitWal(0)
	restartAndCheck("invalid info bits 1234 in .*, LSN .*,", "xlp_info bad", func() {
		writeWal(end, recordHeader(big, 42, uint64(prev)))
		writeWal(startOfNextPage(end), pageHeader(xlpPageMagic, 0x1234, 1, uint64(startOfNextPage(end)), 0))
	})

	emitWal(0)
	prev = advanceOut()
	end = emitWal(0)
	restartAndCheck("there is no contrecord flag at .*", "xlp_info lacks XLP_FIRST_IS_CONTRECORD", func() {
		writeWal(end, recordHeader(big, 42, uint64(prev)))
		writeWal(startOfNextPage(end), pageHeader(xlpPageMagic, 0, 1, uint64(startOfNextPage(end)), 0))
	})

	emitWal(0)
	prev = advanceOut()
	end = emitWal(0)
	restartAndCheck("invalid contrecord length 123456 .* at .*", "xlp_rem_len bad", func() {
		writeWal(end, recordHeader(big, 42, uint64(prev)))
		writeWal(startOfNextPage(end), pageHeader(xlpPageMagic, xlpFirstIsContrecord, 1, uint64(startOfNextPage(end)), 123456))
	})

	// Multi-page, but header is split, so page checks are done first.
	emitWal(0)
	end = advanceTo()
	restartAndCheck("invalid magic number 0000 .* LSN .*", "xlp_magic zero (split record header)",
		func() { writeWal(end, recordHeader(big, 0, 0xdeadbeef)) })

	emitWal(0)
	end = advanceTo()
	restartAndCheck("unexpected pageaddr 0/BAAAAAAD in .*, LSN .*,", "xlp_pageaddr bad (split record header)", func() {
		writeWal(end, recordHeader(big, 0, 0xdeadbeef))
		writeWal(startOfNextPage(end), pageHeader(xlpPageMagic, xlpFirstIsContrecord, 1, 0xbaaaaaad, 0))
	})

	emitWal(0)
	end = advanceTo()
	restartAndCheck("invalid contrecord length 123456 .* at .*", "xlp_rem_len bad (split record header)", func() {
		writeWal(end, recordHeader(big, 0, 0xdeadbeef))
		writeWal(startOfNextPage(end), pageHeader(xlpPageMagic, xlpFirstIsContrecord, 1, uint64(startOfNextPage(end)), 123456))
	})

	node.Stop(tapnode.StopFast)
}
