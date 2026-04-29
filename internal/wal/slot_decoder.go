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
package wal

import (
	"context"
	"errors"
	"fmt"
	"io"
)

// SlotDecoder runs the WAL → reorder buffer → output plugin
// pipeline for one logical slot. Construct one per slot, call
// Run(ctx), Close on ctx cancellation.
type SlotDecoder struct {
	slotName string
	slots    *Slots
	iter     *RecordIterator
	decoder  *Decoder
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
		if len(rec.Payload) > 0 && rec.Payload[0] == RecordKindXactCommit {
			if err := s.slots.AdvanceConfirmedFlushLSN(s.slotName, rec.EndLSN); err != nil {
				return fmt.Errorf("wal: slot %q advance confirmed_flush_lsn: %w", s.slotName, err)
			}
		}
	}
}
