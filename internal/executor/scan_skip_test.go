package executor

import (
	"encoding/binary"
	"strings"
	"testing"
)

// TestScanSkipMatchesFullDecode pins the walk-past of unread columns
// (M0122-0012 slice 19): a seq scan under a non-identity Project decodes only
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
		if len(got) != 2000 {
			t.Fatalf("%s: %d rows, want 2000", q, len(got))
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
