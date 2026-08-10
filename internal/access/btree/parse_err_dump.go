package btree

import (
	"encoding/hex"
	"fmt"
	"os"
	"sync/atomic"
	"time"

	"github.com/goopg/goopg/internal/storage"
)

// btreeParseErrDumpSeq disambiguates dump filenames within one process when
// several parse failures occur (e.g. one per concurrent pgbench backend).
var btreeParseErrDumpSeq int64

// maybeDumpPageOnParseErr writes a forensic snapshot of a B-tree page — its
// decoded opaque header, its full decoded line-pointer table, and a raw hex
// dump of the page — to /tmp/btree-parse-err-<pid>-<seq>.dump whenever an
// item/line-pointer decode fails. Gated on GOOPG_BTREE_PARSE_ERR_DUMP=1 (read
// per call rather than cached, since this only runs on the already-rare parse-
// error path) so it is inert in normal operation. Diagnostic aid for the
// M-NIGHTLY btree keyLen-mismatch corruption investigation (see
// .ralph/deferral_ledger.md) — not permanent instrumentation.
func maybeDumpPageOnParseErr(p storage.Page, ctx string) {
	if os.Getenv("GOOPG_BTREE_PARSE_ERR_DUMP") != "1" {
		return
	}
	seq := atomic.AddInt64(&btreeParseErrDumpSeq, 1)
	path := fmt.Sprintf("/tmp/btree-parse-err-%d-%d.dump", os.Getpid(), seq)
	f, err := os.Create(path)
	if err != nil {
		return
	}
	defer f.Close()

	fmt.Fprintf(f, "context: %s\n", ctx)

	op := readOpaque(p)
	hk, hasHK, _ := pageHighKey(p)
	fmt.Fprintf(f, "opaque: Prev=%d Next=%d Level=%d Flags=0x%x HighKey=%v/%d\n",
		op.Prev, op.Next, op.Level, op.Flags, hasHK, len(hk))

	h := storage.HashPage(p)
	fmt.Fprintf(f, "page content hash: %016x\n", h)
	if os.Getenv("GOOPG_IO_TRACE") == "1" {
		fmt.Fprintf(f, "io trace lifecycle for this exact page content:\n")
		for _, line := range storage.DumpIOTraceForHash(h) {
			fmt.Fprintf(f, "  %s\n", line)
		}
		fmt.Fprintf(f, "io trace, all tags, last 2s before this dump (max 3000 lines):\n")
		for _, line := range storage.DumpRecentIOTrace(2*time.Second, 3000) {
			fmt.Fprintf(f, "  %s\n", line)
		}
	}

	count, cerr := storage.PageLinePointerCount(p)
	fmt.Fprintf(f, "line pointer count: %d (err=%v)\n", count, cerr)
	if cerr == nil {
		for slot := uint16(1); slot <= uint16(count); slot++ {
			id, ierr := storage.PageGetItemID(p, slot)
			if ierr != nil {
				fmt.Fprintf(f, "  slot=%d: itemid decode error: %v\n", slot, ierr)
				continue
			}
			raw, rerr := storage.PageGetItemRaw(p, slot)
			if rerr != nil {
				fmt.Fprintf(f, "  slot=%d off=%d len=%d flags=%d: raw read error: %v\n",
					slot, id.Offset, id.Length, id.Flags, rerr)
				continue
			}
			fmt.Fprintf(f, "  slot=%d off=%d len=%d flags=%d raw=%s\n",
				slot, id.Offset, id.Length, id.Flags, hex.EncodeToString(raw))
		}
	}

	fmt.Fprintf(f, "full page hex dump (%d bytes):\n", len(p))
	fmt.Fprint(f, hex.Dump(p))
}
