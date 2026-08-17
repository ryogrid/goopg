package xlog

// Legacy-format detection (M0014-0004 step 1). See
// docs/design/0014-0004-rollout-guardrails-and-operator-playbook.md.
//
// DetectWALFormat is a read-only utility that inspects the first
// bytes of the first segment file in a pg_wal/ directory and
// classifies the on-disk format. It uses the typed sentinels
// added in M0014-0001 step 1 (ErrInvalidPageHeader) to distinguish
// "PG-compatible page header" from "anything else" — enables the
// step-2 wiring (loadState fail-fast on format mismatch) to stay a
// one-liner.

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
)

// WALFormatVersion identifies the on-disk format of a pg_wal/
// directory. Returned by DetectWALFormat.
type WALFormatVersion int

const (
	// WALFormatUnknown is returned for empty / non-existent
	// directories or directories containing only files that
	// don't parse as either legacy or pg-compat segment names.
	// Treated as "no WAL produced yet" by callers.
	WALFormatUnknown WALFormatVersion = iota

	// WALFormatLegacy is the pre-M0014 goopg format: 8-byte
	// length+CRC32-IEEE record frames, no per-page headers,
	// no XLogRecord chaining.
	WALFormatLegacy

	// WALFormatPGCompat is the M0014 PG18-compatible format:
	// XLogPageHeader at every page boundary, XLogRecord frames
	// with CRC32C, upstream-compatible segment filenames.
	WALFormatPGCompat
)

// String renders the format name for log lines and error messages.
func (v WALFormatVersion) String() string {
	switch v {
	case WALFormatUnknown:
		return "unknown"
	case WALFormatLegacy:
		return "legacy"
	case WALFormatPGCompat:
		return "pgcompat"
	default:
		return fmt.Sprintf("WALFormatVersion(%d)", int(v))
	}
}

// DetectWALFormat inspects the first segment file in walDir and
// returns the on-disk format version. The detection rule:
//
//  1. Read the first 40 bytes (SizeOfXLogLongPHD).
//  2. If they decode as a long-form XLog page header → PGCompat.
//  3. Else if they decode as a short-form XLog page header → PGCompat
//     (operator may have deleted segment 0).
//  4. Else if both header decodes return ErrInvalidPageHeader (wrong
//     magic) → Legacy.
//  5. Filesystem / truncation errors propagate.
//
// CRC validation is intentionally NOT performed — too expensive at
// startup, and the writer's existing per-record validation catches
// real corruption when reading begins. The detector's job is
// classification, not verification.
//
// Empty / nonexistent directories return (WALFormatUnknown, nil) so
// fresh data dirs aren't flagged as errors. Directories containing
// only non-segment files (e.g. a `.gitignore`) likewise return
// Unknown.
func DetectWALFormat(walDir string) (WALFormatVersion, error) {
	entries, err := os.ReadDir(walDir)
	if err != nil {
		if os.IsNotExist(err) {
			return WALFormatUnknown, nil
		}
		return WALFormatUnknown, fmt.Errorf("wal: detect format: %w", err)
	}

	// Find the lowest-numbered file whose name parses as a
	// segment under either format. Both legacy
	// (parseSegmentName) and pg-compat (ParseXLogFileName)
	// produce 24-hex-char names; legacy is a flat sequence
	// number, pg-compat splits into TLI/Log/Seg. The lowest
	// segment is what an operator's pg_wal/ would always have
	// at least one of after any successful Append.
	type segCandidate struct {
		name   string
		segOrd uint64 // ordering key — different semantics per format but consistent within a dir
	}
	var candidates []segCandidate
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		// Try pg-compat first since its parser is strict
		// (strconv.ParseUint with explicit hex bounds). Both
		// parsers return ok=false for non-hex names so
		// non-WAL files (.gitignore, etc.) get skipped here.
		if _, segno, ok := ParseXLogFileName(e.Name(), 0); ok {
			candidates = append(candidates, segCandidate{name: e.Name(), segOrd: segno})
			continue
		}
		if segno, ok := parseSegmentName(e.Name()); ok {
			candidates = append(candidates, segCandidate{name: e.Name(), segOrd: segno})
		}
	}
	if len(candidates) == 0 {
		return WALFormatUnknown, nil
	}
	sort.Slice(candidates, func(i, j int) bool { return candidates[i].segOrd < candidates[j].segOrd })

	first := filepath.Join(walDir, candidates[0].name)
	f, err := os.Open(first)
	if err != nil {
		return WALFormatUnknown, fmt.Errorf("wal: detect format: %w", err)
	}
	defer f.Close()

	var head [SizeOfXLogLongPHD]byte
	n, err := io.ReadFull(f, head[:])
	if err != nil && !errors.Is(err, io.ErrUnexpectedEOF) && !errors.Is(err, io.EOF) {
		return WALFormatUnknown, fmt.Errorf("wal: read %s: %w", first, err)
	}
	if n < SizeOfXLogShortPHD {
		// File is shorter than even a short page header. With
		// the legacy format, an 8-byte length+CRC frame can fit
		// in fewer bytes — but at this size we can't safely
		// classify. Surface the situation so the caller knows.
		return WALFormatUnknown, fmt.Errorf("wal: %s is %d bytes, too short to classify", first, n)
	}

	// Try long-form header first (segment 0's expected shape).
	if _, lerr := DecodeXLogLongPageHeader(head[:]); lerr == nil {
		return WALFormatPGCompat, nil
	}
	// Long-form decode may fail because the long-bit isn't set
	// (page mid-segment, no long header) but the short header is
	// fine. Try short-form.
	if _, serr := DecodeXLogPageHeader(head[:SizeOfXLogShortPHD]); serr == nil {
		return WALFormatPGCompat, nil
	} else if errors.Is(serr, ErrInvalidPageHeader) {
		// Magic mismatch on short-form decode is the canonical
		// "this isn't a PG-compat page" signal — classify as
		// legacy. The legacy format's first bytes are always a
		// length+CRC pair; whatever they encode, they're not
		// XLOGPageMagic by construction (legacy length is at
		// most ~16 MB so the high bytes won't collide).
		return WALFormatLegacy, nil
	} else {
		// Some other decode error (truncation past the slice)
		// — propagate.
		return WALFormatUnknown, fmt.Errorf("wal: %s short-header decode: %w", first, serr)
	}
}
