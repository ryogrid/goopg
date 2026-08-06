package main

import (
	"database/sql"
	"regexp"
	"strings"
	"testing"
	"time"
)

// mkDigest builds a digest from a literal result set. A nil entry is SQL NULL.
func mkDigest(cols []string, rows [][]*string) *resultDigest {
	d := newResultDigest(cols)
	raw := make([]sql.RawBytes, len(cols))
	for _, r := range rows {
		for i := range raw {
			if r[i] == nil {
				raw[i] = nil
				continue
			}
			raw[i] = sql.RawBytes(*r[i])
		}
		d.addRow(raw)
	}
	return d
}

func s(v string) *string { return &v }

// TestDigestCatchesTheP59Rotation is the regression this whole mode exists for.
// P5.9 run 1's defect returned the right NUMBER of rows with every column value
// shifted one relation-block from its name (09 §3.1/§3.2). The two result sets
// below have identical column headers and identical row counts — the only thing
// the old harness compared — and must produce different digests.
func TestDigestCatchesTheP59Rotation(t *testing.T) {
	cols := []string{"c_custkey", "c_name", "o_orderkey", "o_totalprice"}
	correct := mkDigest(cols, [][]*string{
		{s("1"), s("Customer#1"), s("100"), s("42.00")},
		{s("2"), s("Customer#2"), s("200"), s("17.50")},
	})
	rotated := mkDigest(cols, [][]*string{
		{s("Customer#1"), s("100"), s("42.00"), s("1")},
		{s("Customer#2"), s("200"), s("17.50"), s("2")},
	})
	if correct.rows != rotated.rows {
		t.Fatalf("fixture is wrong: row counts must be equal, got %d vs %d", correct.rows, rotated.rows)
	}
	if correct.colsig != rotated.colsig {
		t.Fatalf("fixture is wrong: headers must be equal (the defect is values-only)")
	}
	if correct.unordered == rotated.unordered {
		t.Errorf("unordered digest is blind to a value rotation: %s", hex16(correct.unordered))
	}
	if correct.ordered == rotated.ordered {
		t.Errorf("ordered digest is blind to a value rotation: %s", hex16(correct.ordered))
	}
}

// TestDigestNullIsNotEmptyString: without an explicit NULL marker the two hash
// alike, and a plan that turned a value into NULL would read as a match.
func TestDigestNullIsNotEmptyString(t *testing.T) {
	cols := []string{"a"}
	null := mkDigest(cols, [][]*string{{nil}})
	empty := mkDigest(cols, [][]*string{{s("")}})
	if null.unordered == empty.unordered || null.ordered == empty.ordered {
		t.Errorf("NULL and '' hash alike")
	}
}

// TestDigestFieldsAreLengthPrefixed: a delimiter-joined encoding would make
// ("a","b") and ("ab","") collide for any text column that can contain the
// delimiter. Length prefixes make the encoding unforgeable.
func TestDigestFieldsAreLengthPrefixed(t *testing.T) {
	cols := []string{"a", "b"}
	split := mkDigest(cols, [][]*string{{s("a"), s("b")}})
	joined := mkDigest(cols, [][]*string{{s("ab"), s("")}})
	if split.unordered == joined.unordered {
		t.Errorf(`("a","b") collides with ("ab","")`)
	}
}

// TestUnorderedDigestIsOrderIndependent: the multiset digest is the
// authoritative comparison for a query whose ORDER BY is not a total order,
// so it must survive a row permutation that the ordered digest reports.
func TestUnorderedDigestIsOrderIndependent(t *testing.T) {
	cols := []string{"a", "b"}
	forward := mkDigest(cols, [][]*string{
		{s("1"), s("x")}, {s("2"), s("y")}, {s("3"), s("z")},
	})
	shuffled := mkDigest(cols, [][]*string{
		{s("3"), s("z")}, {s("1"), s("x")}, {s("2"), s("y")},
	})
	if forward.unordered != shuffled.unordered {
		t.Errorf("unordered digest changed under a row permutation")
	}
	if forward.ordered == shuffled.ordered {
		t.Errorf("ordered digest is blind to row order — the two digests would be redundant")
	}
}

// TestUnorderedDigestCountsDuplicates is why the accumulator is a wrapping SUM
// and not an XOR: XOR cancels an identical pair, so a query that emitted a row
// twice would digest like a query that emitted it zero times.
func TestUnorderedDigestCountsDuplicates(t *testing.T) {
	cols := []string{"a"}
	row := []*string{s("7")}
	none := mkDigest(cols, nil)
	once := mkDigest(cols, [][]*string{row})
	twice := mkDigest(cols, [][]*string{row, row})
	if twice.unordered == none.unordered {
		t.Errorf("a duplicated row cancelled itself — accumulator is not duplicate-sensitive")
	}
	if twice.unordered == once.unordered {
		t.Errorf("two copies of a row digest like one")
	}
}

// TestColsigSeparatesHeaderFromValues: a permuted output header is reported as
// SCHEMA-DIFF rather than being folded into a value difference.
func TestColsigSeparatesHeaderFromValues(t *testing.T) {
	same := mkDigest([]string{"a", "b"}, nil)
	swapped := mkDigest([]string{"b", "a"}, nil)
	if same.colsig == swapped.colsig {
		t.Errorf("colsig is blind to a column-name permutation")
	}
	if renamed := mkDigest([]string{"a", "bb"}, nil); same.colsig == renamed.colsig {
		t.Errorf("colsig is blind to a column rename")
	}
	// Length-prefixed here too: "ab"+"" must not collide with "a"+"b".
	if j := mkDigest([]string{"ab", ""}, nil); same.colsig == j.colsig {
		t.Errorf(`colsig collides: ["a","b"] with ["ab",""]`)
	}
}

func TestDigestFieldsFormat(t *testing.T) {
	d := mkDigest([]string{"a"}, [][]*string{{s("1")}})
	got := d.fields()
	for _, want := range []string{"colsig=", "ordered=", "unordered="} {
		if !strings.Contains(got, want) {
			t.Errorf("fields() = %q, missing %s", got, want)
		}
	}
	// Fixed-width hex keeps the tokens column-aligned in a log.
	for _, tok := range strings.Fields(got) {
		_, val, _ := strings.Cut(tok, "=")
		if len(val) != 16 {
			t.Errorf("token %q: want a 16-digit hex value, got %d digits", tok, len(val))
		}
	}
}

// TestOKLineKeepsRowsTerminal pins the output contract three gate scripts
// depend on. scripts/tpch-spotcheck.sh, ci/batch/stages/stage-tpch.sh and
// scripts/tpch-relsize-arm.sh all extract the row count with an end-of-line
// anchor, so a digest token appended AFTER `rows=N` would make each of them
// silently extract the empty string — a gate that reads a blank row count where
// it expected 2 is a gate that no longer checks anything it was written to
// check. The regex below is the gate scripts' own, transcribed.
func TestOKLineKeepsRowsTerminal(t *testing.T) {
	gateRE := regexp.MustCompile(`^Q12: OK .*rows=([0-9]+)$`)
	dig := mkDigest([]string{"a", "b"}, [][]*string{{s("1"), s("2")}})
	for _, tc := range []struct {
		name string
		dig  *resultDigest
	}{
		{"without digest", nil},
		{"with digest", dig},
	} {
		t.Run(tc.name, func(t *testing.T) {
			line := okLine("Q12", "OK", 78930*time.Millisecond, 2, tc.dig)
			m := gateRE.FindStringSubmatch(line)
			if m == nil {
				t.Fatalf("gate regex no longer matches %q", line)
			}
			if m[1] != "2" {
				t.Errorf("gate extracts rows=%q from %q, want \"2\"", m[1], line)
			}
			if !strings.Contains(line, "elapsed=78.93s") {
				t.Errorf("elapsed formatting changed: %q", line)
			}
			if (tc.dig != nil) != strings.Contains(line, "unordered=") {
				t.Errorf("digest tokens misplaced: %q", line)
			}
		})
	}
}

func TestHex16(t *testing.T) {
	for _, tc := range []struct {
		in   uint64
		want string
	}{
		{0, "0000000000000000"},
		{1, "0000000000000001"},
		{0xdeadbeef, "00000000deadbeef"},
		{^uint64(0), "ffffffffffffffff"},
	} {
		if got := hex16(tc.in); got != tc.want {
			t.Errorf("hex16(%d) = %q, want %q", tc.in, got, tc.want)
		}
	}
}
