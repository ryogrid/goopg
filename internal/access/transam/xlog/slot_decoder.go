// Per-slot logical-decoding loop for the M0008 pipeline.
//
// `SlotDecoder` is the long-lived consumer one logical replication
// slot starts up. It owns a `*RecordIterator` anchored at the
// slot's `RestartLSN`, drives a `*Decoder` (and through it, an
// `OutputPlugin`) via the `Classify` dispatch, and advances the
// slot's `ConfirmedFlushLSN` on each successful commit so a
// restart resumes from the right position.
//
// One goroutine per active slot; the loop is sequential by design.
// The slot foundation lives in `slots.go`, the reorder buffer in
// `reorder.go`, the decoder + OutputPlugin contract in
// `decoder.go`, and the per-record classifier in `classifier.go`.
//
// See docs/design/0008-0001-logical-decoding-pipeline.md.
package xlog

import (
	"context"
	"errors"
	"fmt"
	"io"
)

// SlotDecoder runs the WAL → reorder buffer → output plugin
// pipeline for one logical slot. Construct one per slot, call
// Run(ctx), Close on ctx cancellation.
//
// The HISTORIC snapshot bundle (Snapshot field) freezes the
// catalog and MVCC view at slot-decoder construction time so
// plugins can interpret tuple bytes against a stable schema
// shape. Plugins reach the snapshot through whatever surface
// the OutputPlugin implementer wires up — the SlotDecoder
// keeps it as state but doesn't propagate it through the
// `Begin` / `Change` / `Commit` interface today. pgoutput
// (0008-0002) adds that wiring.
type SlotDecoder struct {
	slotName string
	slots    *Slots
	iter     *RecordIterator
	decoder  *Decoder

	// Snapshot is the slot-creation-time HISTORIC view a
	// plugin uses to resolve relations and decide tuple
	// visibility. Set by NewSlotDecoderWithSnapshot;
	// NewSlotDecoder leaves it zero-valued so existing tests
	// stay green.
	Snapshot SlotSnapshot
}

// NewSlotDecoder wires a logical-slot consumer. `slotName` must
// already exist in `slots` (created via `Slots.CreateLogical`).
// The iterator is anchored at the slot's current `RestartLSN`.
// `plugin` receives Begin / Change / Commit calls; pass a real
// pgoutput implementation in production, a recording stub in
// tests.
func NewSlotDecoder(slots *Slots, slotName string, w *Writer, walDir string, segSize int64, plugin OutputPlugin) (*SlotDecoder, error) {
	if slots == nil {
		return nil, errors.New("wal: NewSlotDecoder: nil slots")
	}
	if plugin == nil {
		return nil, errors.New("wal: NewSlotDecoder: nil plugin")
	}
	slot, err := slots.Get(slotName)
	if err != nil {
		return nil, fmt.Errorf("wal: NewSlotDecoder %q: %w", slotName, err)
	}
	if slot.Kind != SlotLogical {
		return nil, fmt.Errorf("%w: slot %q is %q, want %q",
			ErrSlotKindMismatch, slotName, slot.Kind, SlotLogical)
	}
	it, err := NewRecordIterator(w, walDir, segSize, slot.RestartLSN)
	if err != nil {
		return nil, fmt.Errorf("wal: NewSlotDecoder iterator: %w", err)
	}
	return &SlotDecoder{
		slotName: slotName,
		slots:    slots,
		iter:     it,
		decoder:  NewDecoder(plugin),
	}, nil
}

// NewSlotDecoderWithSnapshot is the M0008-aware constructor that
// freezes a HISTORIC catalog + MVCC view at decoder-construction
// time and stuffs it on the returned `SlotDecoder.Snapshot`. The
// snapshot is what a real OutputPlugin (pgoutput in 0008-0002)
// will consult to interpret tuple bytes against a stable schema
// shape; passing the bundle in at construction means the
// plugin's view of the catalog doesn't drift mid-stream.
//
// Mirrors NewSlotDecoder otherwise. See
// docs/design/0008-0001-logical-decoding-pipeline.md.
func NewSlotDecoderWithSnapshot(slots *Slots, slotName string, w *Writer, walDir string, segSize int64, plugin OutputPlugin, snap SlotSnapshot) (*SlotDecoder, error) {
	d, err := NewSlotDecoder(slots, slotName, w, walDir, segSize, plugin)
	if err != nil {
		return nil, err
	}
	d.Snapshot = snap
	return d, nil
}

// Close releases the iterator subscription. Safe to call multiple
// times. Intended to be deferred next to NewSlotDecoder.
func (s *SlotDecoder) Close() error {
	if s == nil || s.iter == nil {
		return nil
	}
	return s.iter.Close()
}

// Run drives the loop until ctx is cancelled or the writer closes.
//
// On every record:
//
//   - Classify dispatches into the reorder buffer / plugin.
//   - For commit records, after a successful plugin Commit, the
//     slot's ConfirmedFlushLSN is advanced to the record's EndLSN.
//     This is the on-disk "subscriber has applied through here"
//     anchor — restart from this LSN never replays a
//     committed-and-acked transaction.
//
// Plugin errors abort the run; the caller decides whether to
// re-create the SlotDecoder. Iterator errors propagate the same
// way. ctx.Cancelled / io.EOF are returned cleanly so a graceful
// shutdown distinguishes itself from a crash.
func (s *SlotDecoder) Run(ctx context.Context) error {
	for {
		rec, err := s.iter.Next(ctx)
		if err != nil {
			if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
				return err
			}
			if errors.Is(err, io.EOF) || errors.Is(err, ErrClosed) {
				return err
			}
			return fmt.Errorf("wal: slot %q decoder iterator: %w", s.slotName, err)
		}
		if err := Classify(s.decoder, rec); err != nil {
			return fmt.Errorf("wal: slot %q classify: %w", s.slotName, err)
		}
		// Advance the slot on commit boundaries. EndLSN is the
		// last byte of the commit record; resuming the iterator
		// from EndLSN+1 (== the next record's StartLSN) is the
		// correct restart anchor. Slots.AdvanceConfirmedFlushLSN
		// rounds up so the on-disk state file always reflects
		// the most recent acked commit.
		if recordIsXactCommit(rec) {
			if err := s.slots.AdvanceConfirmedFlushLSN(s.slotName, rec.EndLSN); err != nil {
				return fmt.Errorf("wal: slot %q advance confirmed_flush_lsn: %w", s.slotName, err)
			}
		}
	}
}

// recordIsXactCommit reports whether rec is a commit record in EITHER of the
// two forms Classify dispatches, and it exists because the two must agree
// (review/260831-2 XL-4): the check used to test the native payload kind only,
// while every commit the server actually writes is PG-format
// (initdb/open.go's xact-marker hook calls EncodeXactCommitPG) and reaches
// ApplyCommit through classifyDecodedXLog. The slot's restart anchor therefore
// never moved and a restart re-delivered every commit since slot creation.
func recordIsXactCommit(rec Record) bool {
	if len(rec.Payload) > 0 {
		return rec.Payload[0] == RecordKindXactCommit
	}
	if rec.XLog == nil {
		return false
	}
	h := rec.XLog.Header
	return h.Rmid == RmgrXact && h.Info&xlogXactOpMask == xlogXactCommit
}
