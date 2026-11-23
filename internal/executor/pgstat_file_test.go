package executor

import (
	"os"
	"path/filepath"
	"testing"
)

// TestPgStatFileRoundTripAndDiscard checks the PG 18 stats-file layout
// round trip and the discard rules of pgstat_read_statsfile: a short file or
// trailing bytes after 'E' discard every entry, and a read removes the file.
// M0149-0007.
func TestPgStatFileRoundTripAndDiscard(t *testing.T) {
	resetAllCumulativeStats()
	defer resetAllCumulativeStats()
	relStats.mu.Lock()
	relStats.shared[16384] = &relStatCounters{numScans: 3, tuplesReturned: 100}
	relStats.mu.Unlock()
	funcStats.mu.Lock()
	funcStats.shared[16390] = &funcStatCounters{calls: 2}
	funcStats.mu.Unlock()
	dbStats.noteFlush(16400, []uint32{16384}, []uint32{16390})

	dir := t.TempDir()
	WritePgStatFile(dir)
	path := filepath.Join(dir, pgstatStatFile)
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	// format id + 6 fixed chunks + 3 hash entries + 'E'.
	want := 4 + (5*6 + 136 + 32 + 88 + 51848 + 512 + 40) + 3*17 + pgstatDBEntryLen + pgstatTabEntryLen + pgstatFuncEntryLen + 1
	if len(data) != want {
		t.Fatalf("stats file is %d bytes, want %d", len(data), want)
	}

	resetAllCumulativeStats()
	RestorePgStatFile(dir)
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("stats file not removed after restore")
	}
	for kind, oid := range map[string]uint64{"database": 0, "relation": 16384, "function": 16390} {
		if ok, _ := haveStats(kind, 16400, oid); !ok {
			t.Errorf("%s stats missing after restore", kind)
		}
	}
	if c, _ := relStats.get(16384); c.numScans != 3 || c.tuplesReturned != 100 {
		t.Errorf("relation counters %+v", c)
	}

	for name, bad := range map[string][]byte{
		"overwrite": []byte("ZZZZZZZZZZZZZ"),
		"append":    append(append([]byte{}, data...), 'X', 'Y', 'Z'),
		"truncated": data[:len(data)-2],
	} {
		if err := os.WriteFile(path, bad, 0o600); err != nil {
			t.Fatal(err)
		}
		RestorePgStatFile(dir)
		if ok, _ := haveStats("relation", 16400, 16384); ok {
			t.Errorf("%s: stats survived an invalid file", name)
		}
		if _, err := os.Stat(path); !os.IsNotExist(err) {
			t.Errorf("%s: invalid stats file not removed", name)
		}
		// restore state for the next case
		if err := os.WriteFile(path, data, 0o600); err != nil {
			t.Fatal(err)
		}
		RestorePgStatFile(dir)
	}
}
