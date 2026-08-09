// Persistent timeline ID for the WAL writer. The TLI is the cluster's
// current timeline identifier (1 on a freshly initialised primary;
// incremented by Promote when a standby takes over).
//
// As of M0130-S8, pg_control's `checkPointCopy.ThisTimeLineID` is the
// single authoritative source of truth. On startup, LoadOrCreateTimelineID
// reads pg_control first; if it exists, its TLI wins. `global/timeline_id`
// is a secondary copy written on promote — if pg_control and the flat file
// disagree (rare: crash between control-file update and the rename), the
// flat file is overwritten to match pg_control. If pg_control is absent
// (fresh initdb still in progress), the flat file is read as a fallback,
// and if that is also absent, the bootstrap TLI (1) is written to both.
//
// See `docs/design/0102-0002-timeline-history-and-promotion-tli-switch.md`
// (M0102-0003) and `docs/design/0130-0008-multi-timeline-streaming-and-timeline-reconciliation.md`
// (M0130-S8) for how this fits into the Promote path.

package initdb

import (
	"encoding/binary"
	"fmt"
	"os"
	"path/filepath"

	"github.com/goopg/goopg/internal/control"
)

// timelineIDFile is the on-disk path (relative to DataDir) where the
// current TLI is stored (secondary copy; pg_control is authoritative).
const timelineIDFile = "global/timeline_id"

// BootstrapTimeLineID is the TLI of a freshly-initialised cluster.
const BootstrapTimeLineID uint32 = 1

// LoadOrCreateTimelineID reads the current TLI with pg_control as the
// authoritative source. Resolution order:
//  1. pg_control CheckPointCopy.ThisTimeLineID (authoritative).
//  2. global/timeline_id flat file (fallback when pg_control is absent).
//  3. BootstrapTimeLineID (1) — fresh cluster.
//
// If pg_control and the flat file disagree, pg_control wins and the flat
// file is corrected. If neither exists, both are initialised to TLI=1.
func LoadOrCreateTimelineID(dataDir string) (uint32, error) {
	cd, ctrlErr := control.ReadControlFile(dataDir)
	if ctrlErr == nil && cd != nil && cd.CheckPointCopyThisTLI > 0 {
		// Authoritative TLI from pg_control: correct the flat file if it
		// disagrees or is absent (crash between control update and rename).
		ctrlTLI := cd.CheckPointCopyThisTLI
		flatTLI, flatErr := readTimelineIDFile(dataDir)
		if flatErr != nil || flatTLI != ctrlTLI {
			if err := WriteTimelineID(dataDir, ctrlTLI); err != nil {
				return 0, fmt.Errorf("goopg: correct timeline_id to match pg_control (tli=%d): %w", ctrlTLI, err)
			}
		}
		return ctrlTLI, nil
	}
	// pg_control absent or unreadable: fall back to the flat file.
	flatTLI, flatErr := readTimelineIDFile(dataDir)
	if flatErr == nil {
		return flatTLI, nil
	}
	// Neither exists: initialise the flat file.
	if err := WriteTimelineID(dataDir, BootstrapTimeLineID); err != nil {
		return 0, err
	}
	return BootstrapTimeLineID, nil
}

// readTimelineIDFile reads the global/timeline_id flat file. Returns
// (0, os.ErrNotExist) when the file is absent.
func readTimelineIDFile(dataDir string) (uint32, error) {
	path := filepath.Join(dataDir, timelineIDFile)
	data, err := os.ReadFile(path)
	if err != nil {
		return 0, err
	}
	if len(data) != 4 {
		return 0, fmt.Errorf("goopg: timeline_id: unexpected length %d", len(data))
	}
	return binary.LittleEndian.Uint32(data), nil
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
