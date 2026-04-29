// Publisher-side LOGICAL walsender: drives the M0008 logical-decoding
// pipeline (`wal.SlotDecoder` + `wal.PgOutput`) on top of the existing
// CopyBoth streaming session and ships each emitted pgoutput message
// to the standby in a `'w'` CopyData frame.
//
// The standby (`internal/server/logicalreceiver.go`) consumes the
// stream and applies each event through `*executor.ApplyWorker`.
// See docs/design/0008-0004-apply-worker-and-tablesync.md.

package server

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/goopg/goopg/internal/catalog"
	"github.com/goopg/goopg/internal/protocol"
	"github.com/goopg/goopg/internal/sqlstate"
	"github.com/goopg/goopg/internal/storage"
	"github.com/goopg/goopg/internal/wal"
)

// runLogicalWalsender is invoked by replyStartReplication after the
// CopyBoth handshake when the client requested `... LOGICAL`. It
// builds a SlotDecoder over the slot, plumbs its OutputPlugin into
// a PgOutput writing through `walsenderPgoutputAdapter`, and runs
// until the standby disconnects or ctx is cancelled.
func (s *Server) runLogicalWalsender(ctx context.Context, r *protocol.FrameReader, w *protocol.FrameWriter, args startReplicationArgs) error {
	if s.cfg.WAL == nil {
		return s.writeStreamingError(w, sqlstate.FeatureNotSupported,
			"START_REPLICATION LOGICAL requires a configured WAL writer")
	}
	if s.cfg.Catalog == nil {
		return s.writeStreamingError(w, sqlstate.FeatureNotSupported,
			"START_REPLICATION LOGICAL requires a configured Catalog")
	}
	im, ok := s.cfg.Catalog.(*catalog.InMemory)
	if !ok {
		return s.writeStreamingError(w, sqlstate.FeatureNotSupported,
			"START_REPLICATION LOGICAL requires *catalog.InMemory backing")
	}
	walDir := s.cfg.WALDirPath
	if walDir == "" {
		return s.writeStreamingError(w, sqlstate.FeatureNotSupported,
			"START_REPLICATION LOGICAL requires Config.WALDirPath to be set")
	}
	segSize := s.cfg.WALSegmentSize
	if segSize <= 0 {
		segSize = wal.DefaultSegmentSize
	}

	// Snapshot the catalog at session start so the pgoutput
	// stream resolves relations against a stable shape; mirrors
	// what the apply worker expects.
	snap := wal.BuildCatalogSnapshot(im)

	// Adapter wraps each pgoutput message in a `'w'` CopyData
	// frame so it lands on the wire in the shape the subscriber's
	// LogicalReceiver consumes.
	adapter := &walsenderPgoutputAdapter{
		w:       w,
		nextLSN: args.StartLSN + 1,
	}
	plugin := wal.NewPgOutput(snap, adapter)

	dec, err := wal.NewSlotDecoderWithSnapshot(s.cfg.Slots, args.SlotName, s.cfg.WAL, walDir, segSize, plugin, wal.SlotSnapshot{Catalog: snap})
	if err != nil {
		return s.writeStreamingError(w, sqlstate.InternalError, err.Error())
	}
	defer dec.Close()

	// Receive-side goroutine: parse standby-status frames so the
	// slot's confirmed_flush_lsn advances. The decoder's commit
	// path also advances it on each commit; the standby-status
	// path mirrors what walreceiver/PHYSICAL slots do so the
	// subscriber ack drives retention.
	streamCtx, streamCancel := context.WithCancel(ctx)
	defer streamCancel()

	receiveDone := make(chan struct{})
	go func() {
		defer close(receiveDone)
		for {
			f, err := r.ReadFrame()
			if err != nil {
				return
			}
			switch f.Type {
			case protocol.MsgCopyData:
				_ = s.handleStandbyCopyData(args.SlotName, f.Payload, nil)
			case protocol.MsgCopyDone, protocol.MsgTerminate:
				streamCancel()
				return
			}
		}
	}()

	runErr := make(chan error, 1)
	go func() { runErr <- dec.Run(streamCtx) }()

	select {
	case <-streamCtx.Done():
		<-receiveDone
		<-runErr
		return nil
	case err := <-runErr:
		streamCancel()
		<-receiveDone
		if errors.Is(err, context.Canceled) || errors.Is(err, wal.ErrClosed) {
			return nil
		}
		if err != nil {
			return s.writeStreamingError(w, sqlstate.InternalError, err.Error())
		}
		return nil
	}
}

// walsenderPgoutputAdapter implements io.Writer by wrapping each
// Write call in a `'w'` CopyData frame and shipping it through the
// FrameWriter. Each PgOutput.Begin/Commit/Change call performs
// exactly one Write so each Write maps to one pgoutput message
// — that's what makes the wire framing work.
//
// nextLSN tracks the byte position the next WAL-data frame's
// startLSN reports. The actual WAL position the publisher's WAL
// writer is at is irrelevant for logical streams; subscribers
// only care that each pgoutput message has a strictly-increasing
// LSN so confirmed_flush_lsn advances monotonically.
type walsenderPgoutputAdapter struct {
	w       *protocol.FrameWriter
	mu      sync.Mutex
	nextLSN uint64
}

func (a *walsenderPgoutputAdapter) Write(p []byte) (int, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	startLSN := a.nextLSN
	endLSN := a.nextLSN + uint64(len(p)) - 1
	a.nextLSN = endLSN + 1
	frame := protocol.EncodeWALData(startLSN, endLSN, time.Now().UTC(), p)
	if err := a.w.WriteCopyData(frame); err != nil {
		return 0, fmt.Errorf("walsender pgoutput write: %w", err)
	}
	if err := a.w.Flush(); err != nil {
		return 0, fmt.Errorf("walsender pgoutput flush: %w", err)
	}
	return len(p), nil
}

// guard the unused-import warning in some build configurations.
var _ = storage.MainFork
