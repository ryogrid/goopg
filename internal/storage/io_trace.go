package storage

import (
	"fmt"
	"hash/fnv"
	"os"
	"sync"
	"time"
)

// TEMPORARY diagnostic for the M-NIGHTLY btree keyLen-mismatch corruption
// investigation (.ralph/deferral_ledger.md). Gated on GOOPG_IO_TRACE=1
// (checked once at package init so it costs nothing when unset). Records a
// content hash of every page at three lifecycle points — immediately before
// evictVictim's flush, immediately after a disk write lands, and immediately
// after a disk read lands — so a corrupted page's exact byte content can be
// traced back to the (RelFileNode, Block) tags it was associated with at
// each point. Not permanent instrumentation; revert once the investigation
// concludes.
var ioTraceEnabled = os.Getenv("GOOPG_IO_TRACE") == "1"

const ioTraceMaxEvents = 4_000_000

type ioTraceEvent struct {
	Tag   BufferTag
	Phase string
	Hash  uint64
	LPCnt int
	LPErr string
	T     time.Time
}

var (
	ioTraceMu  sync.Mutex
	ioTraceLog []ioTraceEvent
)

// HashPage returns a content hash of a page's bytes for cross-referencing
// against the io trace log.
func HashPage(p []byte) uint64 {
	h := fnv.New64a()
	h.Write(p)
	return h.Sum64()
}

func recordIOTrace(tag BufferTag, phase string, page []byte) {
	if !ioTraceEnabled {
		return
	}
	cnt, cerr := PageLinePointerCount(page)
	ev := ioTraceEvent{Tag: tag, Phase: phase, Hash: HashPage(page), LPCnt: cnt, T: time.Now()}
	if cerr != nil {
		ev.LPErr = cerr.Error()
	}
	ioTraceMu.Lock()
	if len(ioTraceLog) < ioTraceMaxEvents {
		ioTraceLog = append(ioTraceLog, ev)
	}
	ioTraceMu.Unlock()
}

// DumpIOTraceForHash returns every recorded trace event whose page content
// hash matches h, formatted for the btree parse-err forensic dump.
func DumpIOTraceForHash(h uint64) []string {
	ioTraceMu.Lock()
	defer ioTraceMu.Unlock()
	var out []string
	for _, ev := range ioTraceLog {
		if ev.Hash == h {
			out = append(out, fmt.Sprintf("tag=%+v phase=%s lpCnt=%d lpErr=%q t=%s",
				ev.Tag, ev.Phase, ev.LPCnt, ev.LPErr, ev.T.Format(time.RFC3339Nano)))
		}
	}
	return out
}

// DumpRecentIOTrace returns up to maxLines recorded trace events within
// window of now, across all tags/hashes (most recent last), formatted for
// the btree parse-err forensic dump. Used to see the full write/flush/read
// activity that preceded a corruption, not just activity matching the
// corrupted page's own content. maxLines bounds output size under heavy
// concurrent load.
func DumpRecentIOTrace(window time.Duration, maxLines int) []string {
	ioTraceMu.Lock()
	defer ioTraceMu.Unlock()
	cutoff := time.Now().Add(-window)
	// Scan backward from the end (most recent) so a maxLines cap keeps the
	// events closest to the crash rather than the start of the window.
	var out []string
	for i := len(ioTraceLog) - 1; i >= 0 && len(out) < maxLines; i-- {
		ev := ioTraceLog[i]
		if ev.T.Before(cutoff) {
			break
		}
		out = append(out, fmt.Sprintf("tag=%+v phase=%s hash=%016x lpCnt=%d lpErr=%q t=%s",
			ev.Tag, ev.Phase, ev.Hash, ev.LPCnt, ev.LPErr, ev.T.Format(time.RFC3339Nano)))
	}
	return out
}
