// pglz.go — PGLZ-compressed varlena construction for bootstrap pg_node_tree
// fields (pg_rewrite.ev_qual / ev_action).
//
// PG stores large varlena values (ev_action, ev_qual) as inline-compressed
// data using its PGLZ algorithm (src/common/pg_lzcompress.c). The compressed
// on-disk format is:
//
//	[4-byte va_header] [4-byte va_tcinfo] [compressed payload]
//
// The actual PGLZ compression, decompression, and varlena framing live in the
// leaf package internal/pglz, which is a faithful port of the PostgreSQL wire
// format so that a real PostgreSQL (e.g. an attached standby) can decompress
// these bootstrap blobs and goopg can decode PostgreSQL-produced compressed
// varlenas (see internal/executor/codec.go and internal/wal/pgoutput.go).

package initdb

import (
	"github.com/goopg/goopg/internal/executor"
	"github.com/goopg/goopg/internal/pglz"
)

// pglzToastThreshold is the minimum size at which PG applies inline TOAST
// compression. Matches TOAST_TUPLE_THRESHOLD from PG18 heaptoast.h (2KB).
const pglzToastThreshold = 2048

// pglzVarlenaDatum returns a Datum for a pg_node_tree column. If the text
// payload is large enough to benefit from compression, it returns a KindBytes
// datum whose BytesValue() is the complete inline-compressed varlena (va_header
// + va_tcinfo + compressed payload). The codec's pg_node_tree KindBytes
// passthrough emits those bytes verbatim.
//
// For small payloads (or when compression does not shrink the value) the
// function falls back to a plain string datum so the codec encodes it as an
// uncompressed 1-byte or 4-byte varlena.
func pglzVarlenaDatum(s string) executor.Datum {
	data := []byte(s)
	if len(data) < pglzToastThreshold {
		return executor.NewStringDatum(s)
	}
	compressed := pglz.Compress(data)
	if 8+len(compressed) >= len(data)+4 {
		// Compression didn't help; store an uncompressed 4-byte varlena.
		return executor.NewStringDatum(s)
	}
	return executor.NewBytesDatum(pglz.BuildCompressedVarlena(compressed, len(data)))
}
