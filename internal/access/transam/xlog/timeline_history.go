// Timeline history files (`<TLI>.history`) record the chain of past
// timelines so a (re-)attaching standby can verify that the LSN
// range it has replayed is consistent with the new primary's view of
// history. Each file's name is the *new* TLI in upper-case 8-hex
// format, matching upstream's `TLHistoryFileName` (see
// `postgres/src/backend/access/transam/timeline.c:463`
// `writeTimeLineHistoryFile`).
//
// On-disk format mirrors upstream exactly: one line per ancestor
// timeline, fields tab-separated:
//
//	<parent_tli>\t<switch_lsn_hex>\t<reason text>\n
//
// Where `switch_lsn_hex` is upstream's `X/X` notation
// (high32_hex/low32_hex). Entries are sorted oldest first; the
// last line is always the most recent switch (i.e. the previous
// timeline's end LSN). PG accepts trailing comments after a `#`
// but writes nothing past the reason; this code only reads/writes
// the canonical three-field shape and therefore round-trips with
// PG-produced files for the common case.

package xlog

import (
	"bufio"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// TimelineHistoryEntry is one line of a `.history` file.
type TimelineHistoryEntry struct {
	// TLI is the parent timeline this line refers to.
	TLI uint32
	// SwitchLSN is the byte position at which TLI ended (the
	// new timeline's first record starts at SwitchLSN).
	SwitchLSN uint64
	// Reason is the free-text reason for the switch — usually
	// "no recovery target specified" for a plain promote.
	Reason string
}

// TimelineHistoryFileName returns the upstream-compatible filename
// (`<TLI>.history`, 8-hex-uppercase TLI) for the given timeline.
func TimelineHistoryFileName(tli uint32) string {
	return fmt.Sprintf("%08X.history", tli)
}

// ReadHistory loads `<walDir>/<tli>.history` and returns the parsed
// entries in file order (oldest first). A missing file returns
// (nil, nil) — upstream treats TLI=1 as "no history" rather than as
// an error, and this matches that contract for any TLI whose history
// hasn't been written yet.
func ReadHistory(walDir string, tli uint32) ([]TimelineHistoryEntry, error) {
	path := filepath.Join(walDir, TimelineHistoryFileName(tli))
	f, err := os.Open(path)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, nil
		}
		return nil, fmt.Errorf("wal: open timeline history %s: %w", path, err)
	}
	defer f.Close()

	var entries []TimelineHistoryEntry
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := strings.TrimRight(sc.Text(), "\r")
		// Strip an optional `#` comment tail; PG never writes one
		// but the format docs say it's allowed.
		if hashIdx := strings.Index(line, "#"); hashIdx >= 0 {
			line = line[:hashIdx]
		}
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) < 2 {
			return nil, fmt.Errorf("wal: timeline history %s: malformed line %q", path, sc.Text())
		}
		parsedTLI, err := strconv.ParseUint(fields[0], 10, 32)
		if err != nil {
			return nil, fmt.Errorf("wal: timeline history %s: parse TLI %q: %w", path, fields[0], err)
		}
		switchLSN, err := parseHistoryLSN(fields[1])
		if err != nil {
			return nil, fmt.Errorf("wal: timeline history %s: parse switch LSN %q: %w", path, fields[1], err)
		}
		reason := ""
		if len(fields) >= 3 {
			// Reason can contain spaces; rejoin the rest of the line
			// after the first two whitespace-delimited tokens.
			afterLSN := strings.TrimLeft(line[len(fields[0]):], " \t")
			afterLSN = strings.TrimLeft(afterLSN[len(fields[1]):], " \t")
			reason = afterLSN
		}
		entries = append(entries, TimelineHistoryEntry{
			TLI:       uint32(parsedTLI),
			SwitchLSN: switchLSN,
			Reason:    reason,
		})
	}
	if err := sc.Err(); err != nil {
		return nil, fmt.Errorf("wal: read timeline history %s: %w", path, err)
	}
	return entries, nil
}

// WriteHistory atomically writes `<walDir>/<tli>.history` containing
// the supplied entries (one per line) using a write-to-tmp + rename
// to survive a crash mid-write. Mode 0o600 mirrors upstream.
//
// Callers should pass the new timeline ID (the one being created),
// not the most recent ancestor — i.e., when promoting a standby from
// TLI 1 to TLI 2, call WriteHistory(walDir, 2, [{TLI:1, ...}]).
func WriteHistory(walDir string, tli uint32, entries []TimelineHistoryEntry) error {
	if err := os.MkdirAll(walDir, 0o700); err != nil {
		return fmt.Errorf("wal: mkdir %s: %w", walDir, err)
	}
	path := filepath.Join(walDir, TimelineHistoryFileName(tli))
	tmp := path + ".tmp"

	var buf strings.Builder
	for _, e := range entries {
		// Format each line as `<TLI>\t<X/X switch LSN>\t<reason>\n`.
		// Empty reason is allowed (upstream emits the field anyway
		// for non-promote switches; we mirror that shape).
		fmt.Fprintf(&buf, "%d\t%X/%X\t%s\n",
			e.TLI,
			uint32(e.SwitchLSN>>32), uint32(e.SwitchLSN),
			e.Reason)
	}

	if err := os.WriteFile(tmp, []byte(buf.String()), 0o600); err != nil {
		return fmt.Errorf("wal: write timeline history %s: %w", tmp, err)
	}
	if err := os.Rename(tmp, path); err != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("wal: rename timeline history %s -> %s: %w", tmp, path, err)
	}
	// fsync the directory so the rename is durable. Best-effort: if
	// the platform doesn't allow opening a directory we skip.
	if dir, err := os.Open(walDir); err == nil {
		_ = dir.Sync()
		_ = dir.Close()
	}
	return nil
}

// parseHistoryLSN parses upstream's `X/X` hex notation. Empty / "0/0"
// → 0. Used for the switch_lsn field of a history line.
func parseHistoryLSN(s string) (uint64, error) {
	parts := strings.SplitN(s, "/", 2)
	if len(parts) != 2 {
		return 0, fmt.Errorf("invalid LSN %q (want X/X)", s)
	}
	hi, err := strconv.ParseUint(parts[0], 16, 32)
	if err != nil {
		return 0, fmt.Errorf("invalid LSN %q: %w", s, err)
	}
	lo, err := strconv.ParseUint(parts[1], 16, 32)
	if err != nil {
		return 0, fmt.Errorf("invalid LSN %q: %w", s, err)
	}
	return (hi << 32) | lo, nil
}
