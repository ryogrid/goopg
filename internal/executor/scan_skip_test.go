package executor

import (
	"encoding/binary"
	"strings"
	"testing"
)

// TestScanSkipMatchesFullDecode pins the walk-past of unread columns
// (M0122-0012 slice 19; filtered scans since slice 29, whose qual columns join
// the read set): a seq scan under a non-identity Project decodes only
// the columns the Project's targets read and steps over the others by their
// stored length (skipAttrLen). Every type below sits before a column that is
// read, so a wrong step for any of them would misplace every later column.
// With tail poison armed, each result must equal the full decode's.
func TestScanSkipMatchesFullDecode(t *testing.T) {
	ctx := deformW8Fixture(t)
	runSQL(t, ctx, `CREATE TABLE sk (
		i2 int2, i4 int4, i8 int8, n numeric, tx text, vc varchar(10), ch char(4),
		d date, ts timestamp, tz timestamptz, f4 float4, f8 float8, b bool,
		u uuid, iv interval, by bytea, big text, last int)`)
	runSQL(t, ctx, `INSERT INTO sk SELECT
		x::int2, x * 7, x::int8 * 100000000000, x::numeric * 123456789012345678901234567890,
		CASE WHEN x % 5 = 0 THEN NULL ELSE 'text-' || x END, 'v' || (x % 100), 'c' || (x % 10),
		('2020-01-' || (1 + x % 28))::date, ('2020-01-01 ' || (x % 24) || ':00:00')::timestamp,
		('2020-01-01 ' || (x % 24) || ':30:00+00')::timestamptz, x / 3.0, x / 7.0, x % 2 = 0,
		md5(x::text)::uuid, (x || ' days')::interval, decode(md5(x::text), 'hex'),
		CASE WHEN x % 3 = 0 THEN repeat('compressible-', 400) ELSE 'short' || x END, x
		FROM generate_series(1, 2000) x`)
	queries := []string{
		"SELECT last, i4 FROM sk",
		"SELECT last + 1, n FROM sk",
		"SELECT tx, last FROM sk",
		"SELECT big, last FROM sk",
		"SELECT b, d, last FROM sk",
		// Slice 29: a Filter absorbed into the scan; the qual's columns join
		// the read set, early (prefilter) and late (a qual on the last
		// column gets no prefilter) alike.
		"SELECT last, i4 FROM sk WHERE i2 % 3 = 1",
		"SELECT tx, n FROM sk WHERE last > 1500",
		"SELECT big FROM sk WHERE d > DATE '2020-01-14' AND f8 < 200",
	}
	for _, q := range queries {
		plan, err := testPlanDeform(t, ctx, q)
		if err != nil {
			t.Fatalf("%s: %v", q, err)
		}
		op := mustBuildDeform(t, plan)
		var scan *seqScanOp
		for cur := Operator(op); cur != nil && scan == nil; {
			if so := unwrapSeqScanOp(cur); so != nil {
				scan = so
				break
			}
			switch o := cur.(type) {
			case *projectOp:
				cur = o.child
			case *instrumentedOp:
				cur = o.inner
			default:
				cur = nil
			}
		}
		if scan == nil || scan.readCols == nil {
			t.Fatalf("%s: the scan records no read set; nothing is exercised", q)
		}
		// The skip flags are set in Open (after the SSI hooks resolve); a
		// read set that never arms one exercises nothing (slice 29: an
		// absorbed Filter's ssiGistPred once disarmed every filtered scan).
		if err := op.Open(ctx); err != nil {
			t.Fatal(err)
		}
		armed := false
		for _, ci := range scan.colInfo {
			armed = armed || ci.skip
		}
		_ = op.Close()
		if !armed {
			t.Fatalf("%s: no column is skipped after Open; nothing is exercised", q)
		}
		old := seqScanDeformPoison
		seqScanDeformPoison = true
		got := sortedRowValues(t, ctx, q)
		seqScanDeformPoison = old
		scanSkipDisabled = true
		want := sortedRowValues(t, ctx, q)
		scanSkipDisabled = false
		if strings.Join(got, "|") != strings.Join(want, "|") {
			t.Fatalf("%s:\nskipping %v\nfull     %v", q, got[:min(5, len(got))], want[:min(5, len(want))])
		}
		if len(got) == 0 || (!strings.Contains(q, "WHERE") && len(got) != 2000) {
			t.Fatalf("%s: %d rows", q, len(got))
		}
	}
}

// TestPhysicalPGVarlenaSize pins the header-only varlena sizing the walk-past
// uses: the short form, the 4-byte form, the compressed 4-byte form (its size
// counts the compressed bytes stored), and refusal of an external pointer.
func TestPhysicalPGVarlenaSize(t *testing.T) {
	short := []byte{byte(5<<1 | 1), 'a', 'b', 'c', 'd', 0xAA}
	if n, ok := physicalPGVarlenaSize(short); !ok || n != 5 {
		t.Errorf("short varlena: %d %v, want 5", n, ok)
	}
	long := make([]byte, 300)
	binary.LittleEndian.PutUint32(long[:4], 200<<2)
	if n, ok := physicalPGVarlenaSize(long); !ok || n != 200 {
		t.Errorf("4-byte varlena: %d %v, want 200", n, ok)
	}
	comp := make([]byte, 300)
	binary.LittleEndian.PutUint32(comp[:4], 120<<2|0x02)
	if n, ok := physicalPGVarlenaSize(comp); !ok || n != 120 {
		t.Errorf("compressed varlena: %d %v, want 120", n, ok)
	}
	if _, ok := physicalPGVarlenaSize([]byte{0x01, 18, 0, 0}); ok {
		t.Error("an external TOAST pointer was measured; it must decline")
	}
	if _, ok := physicalPGVarlenaSize(long[:100]); ok {
		t.Error("a truncated varlena was measured")
	}
}

// TestScanPrefilterSkipMatchesFullDecode pins the prefilter's skipping first
// pass (M0122-0012 slice 25): a qual judged early decodes only its own
// columns of the prefix, stepping over the others by stored length
// (prefilterSkipInfo), and a survivor is decoded again from column 0 so its
// skipped cells never reach the Project. The quals read late columns after
// every type in the table, the select lists read the skipped columns, and
// tail poison is armed; each result must equal the result with the skip off
// and with the early path off.
func TestScanPrefilterSkipMatchesFullDecode(t *testing.T) {
	ctx := deformW8Fixture(t)
	runSQL(t, ctx, `CREATE TABLE skq (
		i2 int2, i4 int4, i8 int8, n numeric, tx text, vc varchar(10), ch char(4),
		d date, ts timestamp, tz timestamptz, f4 float4, f8 float8, b bool,
		u uuid, iv interval, by bytea, big text, last int, flag char(1))`)
	runSQL(t, ctx, `INSERT INTO skq SELECT
		x::int2, x * 7, x::int8 * 100000000000, x::numeric * 123456789012345678901234567890,
		CASE WHEN x % 5 = 0 THEN NULL ELSE 'text-' || x END, 'v' || (x % 100), 'c' || (x % 10),
		('2020-01-' || (1 + x % 28))::date, ('2020-01-01 ' || (x % 24) || ':00:00')::timestamp,
		('2020-01-01 ' || (x % 24) || ':30:00+00')::timestamptz, x / 3.0, x / 7.0, x % 2 = 0,
		md5(x::text)::uuid, (x || ' days')::interval, decode(md5(x::text), 'hex'),
		CASE WHEN x % 3 = 0 THEN repeat('compressible-', 400) ELSE 'short' || x END, x,
		chr(65 + x % 4)
		FROM generate_series(1, 2000) x`)
	for _, q := range []string{
		"SELECT i4, n, tx, d FROM skq WHERE last % 7 = 3",
		"SELECT big, ch, iv FROM skq WHERE last < 300 AND i2 > 10",
		"SELECT u, by, f8, last FROM skq WHERE ch = 'c3'",
		"SELECT tz, vc FROM skq WHERE b AND last > 1500",
		"SELECT n, tx, by FROM skq WHERE ch = ANY ('{c1,c4}'::bpchar[]) AND last > 100",
	} {
		plan, err := testPlanDeform(t, ctx, q)
		if err != nil {
			t.Fatalf("%s: %v", q, err)
		}
		op := mustBuildDeform(t, plan)
		if err := op.Open(ctx); err != nil {
			t.Fatal(err)
		}
		scans := seqScans(op)
		armed := len(scans) == 1 && scans[0].pfInfo != nil
		_ = op.Close()
		if !armed {
			t.Fatalf("%s: the prefilter has no skipping first pass; nothing is exercised", q)
		}
		old := seqScanDeformPoison
		seqScanDeformPoison = true
		got := sortedRowValues(t, ctx, q)
		seqScanDeformPoison = old
		scanSkipDisabled = true
		want := sortedRowValues(t, ctx, q)
		scanSkipDisabled = false
		scanPrefilterOn.Store(false)
		late := sortedRowValues(t, ctx, q)
		scanPrefilterOn.Store(true)
		if strings.Join(got, "|") != strings.Join(want, "|") || strings.Join(got, "|") != strings.Join(late, "|") {
			t.Fatalf("%s: skipping %d rows, full %d, late %d\nskipping %v\nfull     %v", q, len(got), len(want), len(late),
				got[:min(3, len(got))], want[:min(3, len(want))])
		}
		if len(got) == 0 {
			t.Fatalf("%s: no rows", q)
		}
	}
}
