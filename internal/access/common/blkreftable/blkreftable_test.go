package blkreftable

import (
	"bufio"
	"bytes"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

// The golden files under testdata pin byte-identity with PostgreSQL 18.3.
// Each <name>.script is a list of operations; <name>.brt is what PG's own
// src/common/blkreftable.c serialized after running them, and <name>.txt is
// what PG's pg_walsummary printed for that file. The bytes were produced by
// linking libpgcommon.a (postgres/local_install) into a small frontend
// harness:
//
//	M spc db rel fork blk        BlockRefTableMarkBlockModified
//	L spc db rel fork limit      BlockRefTableSetLimitBlock
//	R spc db rel fork from to    mark every block in [from, to)
//
// then WriteBlockRefTable to stdout. The harness is testdata/brt_harness.c;
// regenerate with (from the repository root, P=postgres):
//
//	gcc -I$P/src/include -o brt_harness brt_harness.c \
//	    $P/local_install/lib/libpgcommon.a $P/local_install/lib/libpgport.a
//	./brt_harness < x.script > x.brt && pg_walsummary x.brt > x.txt
//
// The cases cover
// array order and dedup, the bitmap switch at the 4096th offset, limits
// inside an array, inside a bitmap and at its start (an all-zero bitmap is
// still written), trailing-chunk trimming with a kept zero chunk in the
// middle, a dropped bitmap chunk restarting as an array, a limit that may
// not rise, the tablespace-first sort, and a 6000-operation random mix.
var goldenCases = []string{
	"empty",
	"limit_only",
	"array_order_dedup",
	"array_4095",
	"bitmap_at_4096",
	"limit_in_bitmap",
	"limit_zero_bitmap",
	"limit_trims_trailing",
	"limit_in_array",
	"dropped_bitmap_remarked",
	"limit_not_raised",
	"sort_order",
	"random_mix",
}

// runScript replays a golden script against a fresh table.
func runScript(t *testing.T, name string) *Table {
	t.Helper()
	f, err := os.Open(filepath.Join("testdata", name+".script"))
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	tab := NewTable()
	sc := bufio.NewScanner(f)
	for line := 1; sc.Scan(); line++ {
		var op string
		var spc, db, rel, a, b uint32
		var fork int32
		fields := strings.Fields(sc.Text())
		if len(fields) == 0 {
			continue
		}
		n, err := fmt.Sscan(sc.Text(), &op, &spc, &db, &rel, &fork, &a)
		if err != nil || n != 6 {
			t.Fatalf("%s.script:%d: %q: %v", name, line, sc.Text(), err)
		}
		loc := RelFileLocator{SpcOid: spc, DbOid: db, RelNumber: rel}
		switch op {
		case "M":
			tab.MarkBlockModified(loc, fork, a)
		case "L":
			tab.SetLimitBlock(loc, fork, a)
		case "R":
			if len(fields) != 7 {
				t.Fatalf("%s.script:%d: R needs a range end", name, line)
			}
			fmt.Sscan(fields[6], &b)
			for x := a; x < b; x++ {
				tab.MarkBlockModified(loc, fork, x)
			}
		default:
			t.Fatalf("%s.script:%d: unknown op %q", name, line, op)
		}
	}
	if err := sc.Err(); err != nil {
		t.Fatal(err)
	}
	return tab
}

func readGolden(t *testing.T, name, ext string) []byte {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("testdata", name+ext))
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// firstDiff reports the first differing byte offset, for a readable failure.
func firstDiff(a, b []byte) int {
	for i := 0; i < len(a) && i < len(b); i++ {
		if a[i] != b[i] {
			return i
		}
	}
	return min(len(a), len(b))
}

// TestWriteMatchesPostgres requires both writers — the whole-table Write and
// the entry-at-a-time Writer — to produce PG's bytes exactly.
func TestWriteMatchesPostgres(t *testing.T) {
	for _, name := range goldenCases {
		t.Run(name, func(t *testing.T) {
			want := readGolden(t, name, ".brt")
			tab := runScript(t, name)

			var got bytes.Buffer
			if err := tab.Write(&got); err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(got.Bytes(), want) {
				t.Fatalf("Write: %d bytes, PG wrote %d; first difference at byte %d",
					got.Len(), len(want), firstDiff(got.Bytes(), want))
			}

			var inc bytes.Buffer
			w := NewWriter(&inc)
			for _, e := range tab.SortedEntries() {
				w.WriteEntry(e)
			}
			if err := w.Close(); err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(inc.Bytes(), want) {
				t.Fatalf("Writer: %d bytes, PG wrote %d; first difference at byte %d",
					inc.Len(), len(want), firstDiff(inc.Bytes(), want))
			}
		})
	}
}

var forkNames = []string{"main", "fsm", "vm", "init"}

// walsummaryText formats a serialized table the way pg_walsummary prints it
// without -i (dump_one_relation): the limit line when there is a limit, then
// the sorted blocks folded into ranges.
func walsummaryText(t *testing.T, data []byte, name string) string {
	t.Helper()
	rd, err := NewReader(bytes.NewReader(data), name)
	if err != nil {
		t.Fatal(err)
	}
	var out strings.Builder
	for {
		key, limit, ok, err := rd.NextRelation()
		if err != nil {
			t.Fatal(err)
		}
		if !ok {
			break
		}
		prefix := fmt.Sprintf("TS %d, DB %d, REL %d, FORK %s", key.Locator.SpcOid, key.Locator.DbOid,
			key.Locator.RelNumber, forkNames[key.Fork])
		if limit != InvalidBlockNumber {
			fmt.Fprintf(&out, "%s: limit %d\n", prefix, limit)
		}
		var blocks []BlockNumber
		for {
			n := len(blocks)
			// A small batch size exercises resuming inside a chunk.
			blocks, err = rd.Blocks(blocks, 7)
			if err != nil {
				t.Fatal(err)
			}
			if len(blocks) == n {
				break
			}
		}
		sort.Slice(blocks, func(i, j int) bool { return blocks[i] < blocks[j] })
		for i := 0; i < len(blocks); {
			start, end := blocks[i], blocks[i]
			i++
			for i < len(blocks) && blocks[i] == end+1 {
				end++
				i++
			}
			if start == end {
				fmt.Fprintf(&out, "%s: block %d\n", prefix, start)
			} else {
				fmt.Fprintf(&out, "%s: blocks %d..%d\n", prefix, start, end)
			}
		}
	}
	return out.String()
}

// TestReaderMatchesPgWalsummary reads PG-written files and requires the same
// relations, limits and blocks pg_walsummary printed for them.
func TestReaderMatchesPgWalsummary(t *testing.T) {
	for _, name := range goldenCases {
		t.Run(name, func(t *testing.T) {
			got := walsummaryText(t, readGolden(t, name, ".brt"), name)
			if want := string(readGolden(t, name, ".txt")); got != want {
				t.Fatalf("reader output differs from pg_walsummary:\n got: %.400q\nwant: %.400q", got, want)
			}
		})
	}
}

// TestEntryBlocksMatchesReader checks the in-memory Blocks against the
// serialized form, including a window that starts and stops mid-chunk.
func TestEntryBlocksMatchesReader(t *testing.T) {
	tab := runScript(t, "random_mix")
	for _, e := range tab.SortedEntries() {
		all := e.Blocks(0, InvalidBlockNumber, nil)
		var want []BlockNumber
		for _, b := range all {
			if b >= 70000 && b < 140001 {
				want = append(want, b)
			}
		}
		got := e.Blocks(70000, 140001, nil)
		sort.Slice(got, func(i, j int) bool { return got[i] < got[j] })
		sort.Slice(want, func(i, j int) bool { return want[i] < want[j] })
		if fmt.Sprint(got) != fmt.Sprint(want) {
			t.Fatalf("%+v: window [70000,140001) returned %d blocks, filtering all returned %d",
				e.Key(), len(got), len(want))
		}
	}
}

// TestReaderRejectsCorruptFiles pins PG's three reader errors.
func TestReaderRejectsCorruptFiles(t *testing.T) {
	good := readGolden(t, "array_order_dedup", ".brt")
	read := func(data []byte) error {
		rd, err := NewReader(bytes.NewReader(data), "f")
		if err != nil {
			return err
		}
		for {
			_, _, ok, err := rd.NextRelation()
			if err != nil || !ok {
				return err
			}
			for {
				blocks, err := rd.Blocks(nil, 100)
				if err != nil {
					return err
				}
				if len(blocks) == 0 {
					break
				}
			}
		}
	}
	if err := read(good); err != nil {
		t.Fatalf("good file: %v", err)
	}

	badMagic := bytes.Clone(good)
	badMagic[0] ^= 1
	badCRC := bytes.Clone(good)
	badCRC[len(badCRC)-1] ^= 1
	for _, tc := range []struct {
		name string
		data []byte
		want string
	}{
		{"magic", badMagic, `file "f" has wrong magic number: expected 1697321851, found 1697321850`},
		{"checksum", badCRC, `file "f" has wrong checksum`},
		{"truncated", good[:len(good)-10], `file "f" ends unexpectedly`},
	} {
		err := read(tc.data)
		if err == nil || !errors.Is(err, ErrCorrupt) || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%s: got %v, want ErrCorrupt containing %q", tc.name, err, tc.want)
		}
	}
}

// TestPgWalsummaryReadsGoopgFile hands a goopg-written file to PG's own
// pg_walsummary, which validates the magic and CRC, and compares its output
// with the golden text. It skips when the PG client tools are not installed.
func TestPgWalsummaryReadsGoopgFile(t *testing.T) {
	bin := filepath.Join("..", "..", "..", "..", "postgres", "local_install", "bin", "pg_walsummary")
	if _, err := os.Stat(bin); err != nil {
		t.Skipf("pg_walsummary not installed: %v", err)
	}
	for _, name := range []string{"random_mix", "dropped_bitmap_remarked", "sort_order"} {
		var buf bytes.Buffer
		if err := runScript(t, name).Write(&buf); err != nil {
			t.Fatal(err)
		}
		path := filepath.Join(t.TempDir(), name+".summary")
		if err := os.WriteFile(path, buf.Bytes(), 0o600); err != nil {
			t.Fatal(err)
		}
		out, err := exec.Command(bin, path).CombinedOutput()
		if err != nil {
			t.Fatalf("%s: pg_walsummary: %v\n%s", name, err, out)
		}
		if want := string(readGolden(t, name, ".txt")); string(out) != want {
			t.Fatalf("%s: pg_walsummary output on goopg's file differs from the golden", name)
		}
	}
}
