// Standby-mode detection for streaming replication.
//
// PostgreSQL signals standby intent via two trigger files in the
// data directory: `standby.signal` for hot-standby (continuous
// replay + read-only queries) and `recovery.signal` for one-shot
// archive recovery. v0 supports the `standby.signal` flavour;
// `recovery.signal` is documented but unimplemented.
//
// The presence of either file is checked at startup. When
// `standby.signal` exists, `cmd/goopg start` enters standby mode:
// it brings up the regular runtime (storage, MVCC, catalog, WAL
// writer) read-only and dials the primary identified by the
// `primary_conninfo` GUC, spawning a `WalReceiver` to drain
// incoming records.
//
// See docs/design/0005-0001-streaming-replication-architecture.md.
package initdb

import (
	"errors"
	"os"
	"path/filepath"
)

// StandbySignalFile is the filename inside the data directory whose
// presence flips the server into standby mode at startup. Mirrors
// upstream: postgres/src/backend/access/transam/xlogrecovery.c
// uses STANDBY_SIGNAL_FILE = "standby.signal".
const StandbySignalFile = "standby.signal"

// RecoverySignalFile triggers archive-recovery (single-pass replay
// of WAL files supplied via `restore_command`, then promote). v0
// recognises the filename for forward-compat but doesn't yet drive
// the recovery flow.
const RecoverySignalFile = "recovery.signal"

// IsStandby reports whether `<dataDir>/standby.signal` is present.
// A missing data directory or unreadable file returns (false, nil) —
// callers reading the flag at boot want a "not standby" answer in
// the absence of a signal, not a hard error. Other I/O errors are
// surfaced.
func IsStandby(dataDir string) (bool, error) {
	if dataDir == "" {
		return false, nil
	}
	path := filepath.Join(dataDir, StandbySignalFile)
	_, err := os.Stat(path)
	if err == nil {
		return true, nil
	}
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	return false, err
}

// CreateStandbySignal touches `<dataDir>/standby.signal` so the next
// `goopg start` enters standby mode. Used by the (future)
// pg_basebackup-equivalent CLI; tests use it to exercise the
// auto-spawn path.
func CreateStandbySignal(dataDir string) error {
	path := filepath.Join(dataDir, StandbySignalFile)
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
	if err != nil {
		return err
	}
	return f.Close()
}

// RemoveStandbySignal deletes the trigger file. Called by the
// promotion path after the standby has applied all received WAL
// and is converting to primary.
func RemoveStandbySignal(dataDir string) error {
	path := filepath.Join(dataDir, StandbySignalFile)
	if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return nil
}
