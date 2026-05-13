// Persistent timeline ID for the WAL writer. The TLI is the cluster's
// current timeline identifier (1 on a freshly initialised primary;
// incremented by Promote when a standby takes over). Stored as a
// 4-byte little-endian uint32 in `<DataDir>/global/timeline_id`,
// alongside the system identifier (see `LoadOrCreateSystemID`).
//
// Upstream PostgreSQL keeps this value inside `pg_control`'s
// `checkPointCopy.ThisTimeLineID` field, which the recovery driver
// consults at start to seed the writer. Mirroring it in a sibling
// flat file keeps the surface area small while delivering the same
// durability contract: a crash mid-promote leaves the file at its
// previous value (rename-to-final makes the update atomic), so
// recovery never picks up a half-written TLI.
//
// See `docs/design/0102-0002-timeline-history-and-promotion-tli-switch.md`
// (M0102-0003) for how this fits into the Promote path.

package initdb

import (
	"encoding/binary"
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

// timelineIDFile is the on-disk path (relative to DataDir) where the
// current TLI is stored.
const timelineIDFile = "global/timeline_id"

// LoadOrCreateTimelineID reads `<dataDir>/global/timeline_id` and
// returns the persisted TLI. If the file is absent (fresh cluster
// or pre-M0102 cluster), it writes 1 — matching upstream's freshly
// initialised default — and returns 1.
func LoadOrCreateTimelineID(dataDir string) (uint32, error) {
	path := filepath.Join(dataDir, timelineIDFile)
	data, err := os.ReadFile(path)
	if err == nil {
		if len(data) != 4 {
			return 0, fmt.Errorf("goopg: timeline_id: unexpected length %d", len(data))
		}
		return binary.LittleEndian.Uint32(data), nil
	}
	if !errors.Is(err, os.ErrNotExist) {
		return 0, fmt.Errorf("goopg: read timeline_id: %w", err)
	}
	if err := WriteTimelineID(dataDir, 1); err != nil {
		return 0, err
	}
	return 1, nil
}

// WriteTimelineID atomically persists tli to
// `<dataDir>/global/timeline_id` via write-tmp + rename. The parent
// directory must exist (initdb creates `global/` during cluster
// bootstrap; subsequent callers from Open / Promote can rely on it).
func WriteTimelineID(dataDir string, tli uint32) error {
	path := filepath.Join(dataDir, timelineIDFile)
	tmp := path + ".tmp"
	var buf [4]byte
	binary.LittleEndian.PutUint32(buf[:], tli)
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return fmt.Errorf("goopg: mkdir for timeline_id: %w", err)
	}
	if err := os.WriteFile(tmp, buf[:], 0o600); err != nil {
		return fmt.Errorf("goopg: write timeline_id: %w", err)
	}
	if err := os.Rename(tmp, path); err != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("goopg: rename timeline_id: %w", err)
	}
	return nil
}
