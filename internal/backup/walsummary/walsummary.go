// Package walsummary is a port of PostgreSQL 18.3's backend/backup/walsummary.c:
// the WAL summary files under pg_wal/summaries, their names, the two list
// filters, the completeness check, and age-based removal. It also carries
// the summarizer's file write (walsummarizer.c SummarizeWAL's temp file plus
// durable_rename), so the WAL summarizer goroutine and a future incremental
// BASE_BACKUP share one implementation (docs/design/0100-0149/
// 0122-0009-wal-summarizer.md §4.1).
//
// The package is a leaf: it imports only blkreftable, so both
// internal/postmaster/walsummarizer and internal/backup can import it.
//
// All LSNs here are PostgreSQL LSNs (XLogRecPtr), not goopg's 1-based record
// positions; the summarizer translates in one place.
package walsummary

import (
	"bufio"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"time"

	"github.com/goopg/goopg/internal/access/common/blkreftable"
)

// Dir is the summaries directory relative to the data directory
// (XLOGDIR "/summaries").
const Dir = "pg_wal/summaries"

// tempName is the file SummarizeWAL writes before renaming it into place.
const tempName = "temp.summary"

// File identifies one summary file (WalSummaryFile): the WAL on timeline TLI
// from Start up to End.
type File struct {
	TLI   uint32
	Start uint64
	End   uint64
}

// Name is the file's name: TLI, start and end LSN as five 8-digit uppercase
// hex fields, then ".summary" (walsummary.c / walsummarizer.c
// "%08X%08X%08X%08X%08X.summary").
func (f File) Name() string {
	return fmt.Sprintf("%08X%08X%08X%08X%08X.summary",
		f.TLI, uint32(f.Start>>32), uint32(f.Start), uint32(f.End>>32), uint32(f.End))
}

// Path is the file's path under dataDir.
func (f File) Path(dataDir string) string {
	return filepath.Join(dataDir, Dir, f.Name())
}

// IsFilename reports whether name looks like a summary file
// (IsWalSummaryFilename): exactly 40 uppercase hex digits followed by
// ".summary". Lowercase hex, temp.summary and anything else is ignored.
func IsFilename(name string) bool {
	n := 0
	for n < len(name) && isUpperHex(name[n]) {
		n++
	}
	return n == 40 && name[40:] == ".summary"
}

func isUpperHex(c byte) bool {
	return (c >= '0' && c <= '9') || (c >= 'A' && c <= 'F')
}

// ParseFilename decodes a summary file name; ok is false when IsFilename
// rejects it.
func ParseFilename(name string) (f File, ok bool) {
	if !IsFilename(name) {
		return File{}, false
	}
	var v [5]uint64
	for i := range v {
		n, err := strconv.ParseUint(name[i*8:i*8+8], 16, 32)
		if err != nil {
			return File{}, false
		}
		v[i] = n
	}
	return File{TLI: uint32(v[0]), Start: v[1]<<32 | v[2], End: v[3]<<32 | v[4]}, true
}

// List returns the summaries on disk that match the filter (GetWalSummaries):
// tli != 0 keeps only that timeline, start != 0 keeps only files that end
// after start, end != 0 keeps only files that start before end. Files are
// returned in name order; PG returns readdir order, which no caller relies on.
func List(dataDir string, tli uint32, start, end uint64) ([]File, error) {
	dents, err := os.ReadDir(filepath.Join(dataDir, Dir))
	if err != nil {
		return nil, fmt.Errorf("could not open directory %q: %w", filepath.Join(dataDir, Dir), err)
	}
	var out []File
	for _, d := range dents {
		f, ok := ParseFilename(d.Name())
		if !ok {
			continue
		}
		if tli != 0 && tli != f.TLI {
			continue
		}
		if start != 0 && start >= f.End {
			continue
		}
		if end != 0 && end <= f.Start {
			continue
		}
		out = append(out, f)
	}
	return out, nil
}

// Filter keeps the entries of list that match (FilterWalSummaries). Unlike
// List, the LSN bounds are inclusive: a file ending exactly at start, or
// starting exactly at end, is kept. Both predicates are PG's, ported as is.
func Filter(list []File, tli uint32, start, end uint64) []File {
	var out []File
	for _, f := range list {
		if tli != 0 && tli != f.TLI {
			continue
		}
		if start != 0 && start > f.End {
			continue
		}
		if end != 0 && end < f.Start {
			continue
		}
		out = append(out, f)
	}
	return out
}

// AreComplete reports whether list covers every LSN from start to end
// (WalSummariesAreComplete). Timelines are ignored. When it is incomplete,
// missing is the first LSN not covered, or 0 (InvalidXLogRecPtr) when list
// is empty. Overlapping summaries are tolerated.
func AreComplete(list []File, start, end uint64) (complete bool, missing uint64) {
	if len(list) == 0 {
		return false, 0
	}
	sorted := append([]File(nil), list...)
	sort.SliceStable(sorted, func(i, j int) bool { return sorted[i].Start < sorted[j].Start })
	current := start
	for _, f := range sorted {
		if f.Start > current {
			break // a gap
		}
		if f.End > current {
			current = f.End
			if current >= end {
				return true, 0
			}
		}
	}
	return false, current
}

// Open opens a summary file for reading (OpenWalSummaryFile). With missingOK,
// a file that does not exist returns (nil, nil).
func Open(dataDir string, f File, missingOK bool) (*os.File, error) {
	path := f.Path(dataDir)
	fh, err := os.Open(path)
	if err != nil {
		if missingOK && errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		return nil, fmt.Errorf("could not open file %q: %w", path, err)
	}
	return fh, nil
}

// Read opens a summary file and returns a BlockRefTable reader over it. The
// caller closes the returned file.
func Read(dataDir string, f File) (*blkreftable.Reader, *os.File, error) {
	fh, err := Open(dataDir, f, false)
	if err != nil {
		return nil, nil, err
	}
	rd, err := blkreftable.NewReader(bufio.NewReader(fh), fh.Name())
	if err != nil {
		fh.Close()
		return nil, nil, err
	}
	return rd, fh, nil
}

// RemoveIfOlderThan deletes the summary file when its modification time,
// in whole seconds as time_t compares, precedes cutoff
// (RemoveWalSummaryIfOlderThan). A file that is already gone is not an
// error.
func RemoveIfOlderThan(dataDir string, f File, cutoff time.Time) error {
	path := f.Path(dataDir)
	st, err := os.Lstat(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return fmt.Errorf("could not stat file %q: %w", path, err)
	}
	if st.ModTime().Unix() >= cutoff.Unix() {
		return nil
	}
	if err := os.Remove(path); err != nil {
		return fmt.Errorf("could not remove file %q: %w", path, err)
	}
	return nil
}

// Write stores table as the summary file f, the way SummarizeWAL does: it
// writes pg_wal/summaries/temp.summary, then durable_renames it to its final
// name. durable_rename fsyncs the old file, renames it, fsyncs the new name
// and then the directory, so a crash leaves either no file or a complete one.
func Write(dataDir string, f File, table *blkreftable.Table) error {
	dir := filepath.Join(dataDir, Dir)
	tempPath := filepath.Join(dir, tempName)
	finalPath := f.Path(dataDir)
	fh, err := os.OpenFile(tempPath, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o600)
	if err != nil {
		return fmt.Errorf("could not create file %q: %w", tempPath, err)
	}
	bw := bufio.NewWriter(fh)
	if err := table.Write(bw); err != nil {
		fh.Close()
		return fmt.Errorf("could not write file %q: %w", tempPath, err)
	}
	if err := bw.Flush(); err != nil {
		fh.Close()
		return fmt.Errorf("could not write file %q: %w", tempPath, err)
	}
	if err := fh.Sync(); err != nil {
		fh.Close()
		return fmt.Errorf("could not fsync file %q: %w", tempPath, err)
	}
	if err := fh.Close(); err != nil {
		return fmt.Errorf("could not close file %q: %w", tempPath, err)
	}
	if err := os.Rename(tempPath, finalPath); err != nil {
		return fmt.Errorf("could not rename file %q to %q: %w", tempPath, finalPath, err)
	}
	if err := fsyncPath(finalPath); err != nil {
		return err
	}
	return fsyncPath(dir)
}

func fsyncPath(path string) error {
	fh, err := os.Open(path)
	if err != nil {
		return fmt.Errorf("could not open file %q: %w", path, err)
	}
	defer fh.Close()
	if err := fh.Sync(); err != nil {
		return fmt.Errorf("could not fsync file %q: %w", path, err)
	}
	return nil
}
