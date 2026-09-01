package backup

import (
	"bytes"
	"context"
	"testing"

	"github.com/goopg/goopg/internal/libpq"
)

// BenchmarkBaseBackupStreamerWrite measures the base-backup CopyData path
// (review/260831 NB-19): the payload buffer used to be allocated per 64 KiB
// chunk.
func BenchmarkBaseBackupStreamerWrite(b *testing.B) {
	var sink bytes.Buffer
	s := &baseBackupStreamer{
		w:                libpq.NewFrameWriter(&sink),
		ctx:              context.Background(),
		nextProgressMark: ^uint64(0),
	}
	payload := make([]byte, 1<<20) // 1 MiB = 16 chunks
	b.ReportAllocs()
	b.SetBytes(int64(len(payload)))
	for b.Loop() {
		sink.Reset()
		if _, err := s.Write(payload); err != nil {
			b.Fatal(err)
		}
	}
}
