package wal

import (
	"encoding/binary"
	"errors"
	"fmt"
	"io/fs"
	"path/filepath"

	"github.com/goopg/goopg/internal/storage"
)

const (
	// RecordKindPageImage is a full-page image redo record.
	RecordKindPageImage byte = 1
	// RecordKindCheckpoint marks a consistent recovery boundary.
	RecordKindCheckpoint byte = 2
	// RecordKindBtreeSplit captures a B-tree page split atomically:
	// the post-split image of the left page plus the freshly
	// populated right page in one record. Replay applies both,
	// guaranteeing crash recovery never lands in the torn state
	// where left advertises a right-link to a page that's still
	// the bare smgr.Extend zero/init image. See
	// docs/design/0002-0002-btree-concurrency.md Landing 3a.
	RecordKindBtreeSplit byte = 3

	pageImageHeaderSize = 14
	// btreeSplitHeaderSize: kind(1) + DBOid(4) + RelOid(4) +
	// Fork(1) + LeftBlk(4) + RightBlk(4) = 18.
	btreeSplitHeaderSize = 18
)

// ReplayStats summarizes one replay run.
type ReplayStats struct {
	Records       int
	Applied       int
	CheckpointLSN uint64
}

// EncodeCheckpoint encodes a checkpoint marker record payload.
func EncodeCheckpoint() []byte {
	return []byte{RecordKindCheckpoint}
}

// EncodePageImage encodes one full-page image record payload.
func EncodePageImage(rel storage.RelFileNode, blk storage.BlockNumber, page storage.Page) ([]byte, error) {
	if len(page) != storage.BlockSize {
		return nil, fmt.Errorf("wal: page image is %d bytes, want %d", len(page), storage.BlockSize)
	}
	out := make([]byte, pageImageHeaderSize+storage.BlockSize)
	out[0] = RecordKindPageImage
	binary.LittleEndian.PutUint32(out[1:5], rel.DBOid)
	binary.LittleEndian.PutUint32(out[5:9], rel.RelOid)
	out[9] = byte(rel.Fork)
	binary.LittleEndian.PutUint32(out[10:14], uint32(blk))
	copy(out[pageImageHeaderSize:], page)
	return out, nil
}

// EncodeBtreeSplit encodes one atomic B-tree split record. Both
// pages must be exactly storage.BlockSize bytes; the record
// embeds them in left-then-right order so replay applies the new
// right page before any reader could follow left's right-link to
// it.
func EncodeBtreeSplit(rel storage.RelFileNode, leftBlk, rightBlk storage.BlockNumber, leftPage, rightPage storage.Page) ([]byte, error) {
	if len(leftPage) != storage.BlockSize {
		return nil, fmt.Errorf("wal: btree-split left page is %d bytes, want %d", len(leftPage), storage.BlockSize)
	}
	if len(rightPage) != storage.BlockSize {
		return nil, fmt.Errorf("wal: btree-split right page is %d bytes, want %d", len(rightPage), storage.BlockSize)
	}
	out := make([]byte, btreeSplitHeaderSize+2*storage.BlockSize)
	out[0] = RecordKindBtreeSplit
	binary.LittleEndian.PutUint32(out[1:5], rel.DBOid)
	binary.LittleEndian.PutUint32(out[5:9], rel.RelOid)
	out[9] = byte(rel.Fork)
	binary.LittleEndian.PutUint32(out[10:14], uint32(leftBlk))
	binary.LittleEndian.PutUint32(out[14:18], uint32(rightBlk))
	copy(out[btreeSplitHeaderSize:btreeSplitHeaderSize+storage.BlockSize], leftPage)
	copy(out[btreeSplitHeaderSize+storage.BlockSize:], rightPage)
	return out, nil
}

// DecodeBtreeSplit returns the rel + (left,right) blocks + page
// images carried by a BtreeSplit record payload.
func DecodeBtreeSplit(payload []byte) (rel storage.RelFileNode, leftBlk, rightBlk storage.BlockNumber, leftPage, rightPage storage.Page, err error) {
	want := btreeSplitHeaderSize + 2*storage.BlockSize
	if len(payload) != want {
		err = fmt.Errorf("wal: invalid btree-split payload len %d (want %d)", len(payload), want)
		return
	}
	if payload[0] != RecordKindBtreeSplit {
		err = fmt.Errorf("wal: record kind %d is not btree-split", payload[0])
		return
	}
	rel = storage.RelFileNode{
		DBOid:  binary.LittleEndian.Uint32(payload[1:5]),
		RelOid: binary.LittleEndian.Uint32(payload[5:9]),
		Fork:   storage.ForkNumber(payload[9]),
	}
	leftBlk = storage.BlockNumber(binary.LittleEndian.Uint32(payload[10:14]))
	rightBlk = storage.BlockNumber(binary.LittleEndian.Uint32(payload[14:18]))
	leftPage = make(storage.Page, storage.BlockSize)
	copy(leftPage, payload[btreeSplitHeaderSize:btreeSplitHeaderSize+storage.BlockSize])
	rightPage = make(storage.Page, storage.BlockSize)
	copy(rightPage, payload[btreeSplitHeaderSize+storage.BlockSize:])
	return
}

// DecodePageImage decodes a full-page image record payload.
func DecodePageImage(payload []byte) (storage.RelFileNode, storage.BlockNumber, storage.Page, error) {
	if len(payload) != pageImageHeaderSize+storage.BlockSize {
		return storage.RelFileNode{}, storage.InvalidBlockNumber, nil,
			fmt.Errorf("wal: invalid page-image payload len %d", len(payload))
	}
	if payload[0] != RecordKindPageImage {
		return storage.RelFileNode{}, storage.InvalidBlockNumber, nil,
			fmt.Errorf("wal: record kind %d is not page image", payload[0])
	}
	rel := storage.RelFileNode{
		DBOid:  binary.LittleEndian.Uint32(payload[1:5]),
		RelOid: binary.LittleEndian.Uint32(payload[5:9]),
		Fork:   storage.ForkNumber(payload[9]),
	}
	blk := storage.BlockNumber(binary.LittleEndian.Uint32(payload[10:14]))
	page := make(storage.Page, storage.BlockSize)
	copy(page, payload[pageImageHeaderSize:])
	return rel, blk, page, nil
}

// ReplayRecords replays decoded WAL records into storage.
func ReplayRecords(mgr *storage.Manager, records []Record) (ReplayStats, error) {
	stats := ReplayStats{Records: len(records)}
	replayUntil, checkpointLSN := replayLimit(records)
	stats.CheckpointLSN = checkpointLSN

	for i, r := range records[:replayUntil] {
		if len(r.Payload) == 0 {
			return stats, fmt.Errorf("wal: replay record %d has empty payload", i)
		}
		switch r.Payload[0] {
		case RecordKindPageImage:
			if err := replayPageImage(mgr, r.Payload); err != nil {
				return stats, fmt.Errorf("wal: replay record %d lsn[%d,%d]: %w", i, r.StartLSN, r.EndLSN, err)
			}
			stats.Applied++
		case RecordKindBtreeSplit:
			if err := replayBtreeSplit(mgr, r.Payload); err != nil {
				return stats, fmt.Errorf("wal: replay record %d lsn[%d,%d]: %w", i, r.StartLSN, r.EndLSN, err)
			}
			stats.Applied++
		case RecordKindCheckpoint:
			// Marker only; no page write.
			continue
		default:
			return stats, fmt.Errorf("wal: replay record %d lsn[%d,%d]: unsupported kind %d", i, r.StartLSN, r.EndLSN, r.Payload[0])
		}
	}
	return stats, nil
}

// ReplayFromDir reads records from <dataDir>/pg_wal and replays them.
func ReplayFromDir(dataDir string, segmentSize int64) (ReplayStats, error) {
	records, err := ReadAll(filepath.Join(dataDir, "pg_wal"), segmentSize)
	if err != nil {
		return ReplayStats{}, err
	}
	mgr := storage.NewManager(storage.ManagerConfig{DataDir: dataDir})
	defer func() { _ = mgr.Close() }()
	return ReplayRecords(mgr, records)
}

// ReplayFromDirWithMgr replays the WAL segments under walDir into
// the supplied Manager. Used by initdb.Open at startup so the
// runtime's single Manager handles both the replay phase and
// subsequent normal I/O. A missing or empty walDir is treated as
// "nothing to replay" (a freshly initdb'd cluster). segmentSize
// of 0 means use the default DefaultSegmentSize.
func ReplayFromDirWithMgr(mgr *storage.Manager, walDir string, segmentSize int64) (ReplayStats, error) {
	if segmentSize == 0 {
		segmentSize = DefaultSegmentSize
	}
	records, err := ReadAll(walDir, segmentSize)
	if err != nil {
		// Missing pg_wal on a fresh data dir is fine — no records
		// to replay.
		if errors.Is(err, fs.ErrNotExist) {
			return ReplayStats{}, nil
		}
		return ReplayStats{}, err
	}
	return ReplayRecords(mgr, records)
}

// replayBtreeSplit applies the left then right page images carried
// by an atomic split record. The right page is applied via Extend
// when the relation is one block short of containing it (the
// freshly-allocated case at original record time) and via
// WriteBlock when the file is already long enough (replay re-run
// or the right page somehow already on disk). Apply order is
// left → right so a reader following left's right-link from the
// post-replay state always finds a real right page on disk.
func replayBtreeSplit(mgr *storage.Manager, payload []byte) error {
	rel, leftBlk, rightBlk, leftPage, rightPage, err := DecodeBtreeSplit(payload)
	if err != nil {
		return err
	}
	if err := writeBlockOrExtend(mgr, rel, leftBlk, leftPage); err != nil {
		return fmt.Errorf("apply left block %d: %w", leftBlk, err)
	}
	if err := writeBlockOrExtend(mgr, rel, rightBlk, rightPage); err != nil {
		return fmt.Errorf("apply right block %d: %w", rightBlk, err)
	}
	return nil
}

// writeBlockOrExtend installs `page` at the given block number,
// extending the relation if the block is exactly one past the end.
// It is the shared kernel for replayPageImage and the per-side
// apply in replayBtreeSplit.
func writeBlockOrExtend(mgr *storage.Manager, rel storage.RelFileNode, blk storage.BlockNumber, page storage.Page) error {
	nblocks, err := mgr.NBlocks(rel)
	if err != nil {
		return err
	}
	switch {
	case blk < nblocks:
		return mgr.WriteBlock(rel, blk, page)
	case blk == nblocks:
		got, err := mgr.Extend(rel, page)
		if err != nil {
			return err
		}
		if got != blk {
			return fmt.Errorf("wal: extend returned block %d, want %d", got, blk)
		}
		return nil
	default:
		return fmt.Errorf("wal: replay gap block=%d nblocks=%d", blk, nblocks)
	}
}

func replayPageImage(mgr *storage.Manager, payload []byte) error {
	rel, blk, page, err := DecodePageImage(payload)
	if err != nil {
		return err
	}
	return writeBlockOrExtend(mgr, rel, blk, page)
}

func replayLimit(records []Record) (int, uint64) {
	lastCheckpointIdx := -1
	var checkpointLSN uint64
	for i, r := range records {
		if len(r.Payload) == 0 {
			continue
		}
		if r.Payload[0] == RecordKindCheckpoint {
			lastCheckpointIdx = i
			checkpointLSN = r.EndLSN
		}
	}
	if lastCheckpointIdx == -1 {
		return len(records), 0
	}
	return lastCheckpointIdx + 1, checkpointLSN
}
