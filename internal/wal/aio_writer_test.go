package wal

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"
)

// recordingWriterAIO captures every Submit so a test can assert
// the WAL writer's writeAt path actually flowed through the
// engine when one was attached. Mirrors the recording engines
// used in internal/storage and internal/executor — duplicated
// because Go test packages can't share helpers.
type recordingWriterAIO struct {
	submits []AIOSubmitOp
}

func (r *recordingWriterAIO) Submit(op AIOSubmitOp) AIOHandle {
	r.submits = append(r.submits, op)
	var n int
	var err error
	switch op.Direction {
	case AIODirWrite:
		n, err = op.File.WriteAt(op.Buffer, op.Offset)
	default:
		n, err = op.File.ReadAt(op.Buffer, op.Offset)
	}
	return recordingWriterAIOHandle{n: n, err: err}
}

type recordingWriterAIOHandle struct {
	n   int
	err error
}

func (h recordingWriterAIOHandle) Wait() (int, error) { return h.n, h.err }

// TestWriterAppendNoAIOPreservesLegacyPath: with no engine in
// Config.AIO, Append goes through f.WriteAt directly. Bytes
// land on disk and Read can find them — substrate-equivalence
// guard for deployments that don't opt into AIO.
func TestWriterAppendNoAIOPreservesLegacyPath(t *testing.T) {
	dir := t.TempDir()
	w, err := NewWriter(Config{WALDir: dir, SegmentSize: 4096})
	if err != nil {
		t.Fatal(err)
	}
	defer w.Close()
	payload := []byte("legacy")
	_, end, err := w.Append(payload)
	if err != nil {
		t.Fatal(err)
	}
	if err := w.FlushUpTo(end); err != nil {
		t.Fatal(err)
	}

	// Confirm bytes hit the segment file.
	data, err := os.ReadFile(filepath.Join(dir, formatSegmentName(0)))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(data, payload) {
		t.Errorf("segment %x missing %q", data, payload)
	}
}

// TestWriterAppendAIOFlowsThroughEngine pins the wired path:
// with an engine in Config.AIO, every writeAt segment-chunk
// flows through Submit (Direction=AIODirWrite) and bytes
// round-trip through the segment file. The flushUpTo path's
// fdatasync barrier still runs after every Submit has Waited,
// so durability semantics are unchanged.
func TestWriterAppendAIOFlowsThroughEngine(t *testing.T) {
	dir := t.TempDir()
	eng := &recordingWriterAIO{}
	w, err := NewWriter(Config{
		WALDir:      dir,
		SegmentSize: 4096,
		AIO:         eng,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer w.Close()

	payload := []byte("through-aio")
	_, end, err := w.Append(payload)
	if err != nil {
		t.Fatal(err)
	}
	if err := w.FlushUpTo(end); err != nil {
		t.Fatal(err)
	}

	if got := len(eng.submits); got == 0 {
		t.Errorf("engine saw 0 submits — Append did not flow through the engine")
	}
	for i, op := range eng.submits {
		if op.Direction != AIODirWrite {
			t.Errorf("submit[%d] direction=%v want AIODirWrite", i, op.Direction)
		}
	}

	data, err := os.ReadFile(filepath.Join(dir, formatSegmentName(0)))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(data, payload) {
		t.Errorf("segment %x missing %q (engine submits=%d)",
			data, payload, len(eng.submits))
	}
}
