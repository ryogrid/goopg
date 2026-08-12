package executor

import (
	"encoding/binary"
	"testing"
	"time"

	"github.com/goopg/goopg/internal/catalog"
	"github.com/goopg/goopg/internal/access/btree"
)

// M0119-0006, 41st slice — the ORIGINS of a timestamptz datum, not its renderer.
//
// The 40th slice gave `timestamp with time zone` a Datum discriminator
// (TimeSubTimestampTZ / NewTimestampTZDatum) and taught the type-agnostic
// renderer behind `::text` to dispatch on it. It tagged only the four producers
// the reported defect flowed through — the typed literal, evalCast and the
// three `prorettype` 1184 functions — and filed a ledger row saying the REST of
// the KindTime producers were not audited, so a timestamptz value that entered
// the executor through some OTHER door still rendered zone-less.
//
// This file is the guard for that audit. The claim it pins is narrow and
// checkable: every decoder that KNOWS the declared SQL type must mint the
// tagged datum, so `col::text` prints the zone no matter which door the value
// came through. The doors below are the ones the ledger named — binary COPY,
// the two index-key decoders (composite and single-column), COPY-text — plus
// pg_authid's rolvaliduntil, whose column type is a compile-time constant.
//
// Note what is deliberately NOT here: the target-type-less paths
// (tryParseStringAs, EXTRACT, date_trunc) have a discriminator to READ but
// nothing threads a declared type into them, and the array-element renderer
// hardcodes UTC — both keep their own ledger rows. A door with no type in reach
// cannot be fixed by tagging.

// tstzOriginInstant is the instant '2020-01-01 10:00:00+05:30' denotes, i.e.
// 04:30:00 UTC. Under TimeZone=Asia/Kolkata a correctly tagged datum renders it
// back as '2020-01-01 10:00:00+05:30'; an untagged one prints the bare UTC wall
// clock '2020-01-01 04:30:00' with no zone at all — a silent relabel.
var tstzOriginInstant = time.Date(2020, 1, 1, 4, 30, 0, 0, time.UTC)

const (
	tstzOriginWantText = "2020-01-01 10:00:00+05:30"
	tstzOriginZone     = "Asia/Kolkata"
)

// pgMicrosOf is the PG timestamp/timestamptz heap and key image of an instant:
// microseconds since 2000-01-01 UTC.
func pgMicrosOf(t time.Time) int64 { return t.UnixMicro() - pgEpochUnixMicros }

// TestTimestampTZOriginsCarryTheSubtypeTag walks each origin that has the
// declared SQL type in reach, decodes the same instant through it, and asserts
// both halves: the datum is tagged, and the type-agnostic `::text` renderer
// therefore agrees with what goopg's own typed SELECT output prints.
//
// Table-driven over the ORIGIN, not over the value: one instant is enough to
// separate "carries the zone" from "does not", and the per-DateStyle spellings
// are already pinned against a real PG 18.3 by
// TestTimestampTZCastToTextMatchesPG18Oracle. What this test adds is coverage
// of the doors.
func TestTimestampTZOriginsCarryTheSubtypeTag(t *testing.T) {
	tstzCol := catalog.Column{Name: "c", Type: catalog.Type{Name: "timestamptz"}}
	micros := pgMicrosOf(tstzOriginInstant)

	binPayload := make([]byte, 8)
	binary.BigEndian.PutUint64(binPayload, uint64(micros))

	origins := []struct {
		name string
		// decode produces the Datum a real read through this door produces.
		decode func(t *testing.T) Datum
	}{
		{
			// COPY … FROM … WITH (FORMAT binary) into a timestamptz column.
			name: "binary COPY decode",
			decode: func(t *testing.T) Datum {
				d, err := copyBinaryToDatum(tstzCol.Type, binPayload)
				if err != nil {
					t.Fatalf("copyBinaryToDatum: %v", err)
				}
				return d
			},
		},
		{
			// COPY … FROM (text format) into a timestamptz column: the zone in
			// the text belongs to the value, so this is the same instant.
			name: "text COPY decode",
			decode: func(t *testing.T) Datum {
				d, err := copyTextToDatum(tstzCol.Type, []byte("2020-01-01 10:00:00+05:30"), "")
				if err != nil {
					t.Fatalf("copyTextToDatum: %v", err)
				}
				return d
			},
		},
		{
			// A composite index-only scan answering the column from the key.
			name: "composite index-key decode",
			decode: func(t *testing.T) Datum {
				d, n, err := decodeIndexKeyColumn(btree.EncodeTimestamp(micros), tstzCol)
				if err != nil {
					t.Fatalf("decodeIndexKeyColumn: %v", err)
				}
				if n != 8 {
					t.Fatalf("decodeIndexKeyColumn consumed %d bytes, want 8", n)
				}
				return d
			},
		},
		{
			// A single-column index-only scan answering from the key. Sibling of
			// the above (Hard-won Rule #2) — they must agree.
			name: "single-column index-key decode",
			decode: func(t *testing.T) Datum {
				d, err := decodeBTreeKeyToDatum(btree.EncodeTimestamp(micros), tstzCol)
				if err != nil {
					t.Fatalf("decodeBTreeKeyToDatum: %v", err)
				}
				return d
			},
		},
	}

	for _, o := range origins {
		t.Run(o.name, func(t *testing.T) {
			d := o.decode(t)
			if !d.IsTimestampTZ() {
				t.Errorf("%s produced an untagged KindTime datum (TimeSub=%d); a timestamptz "+
					"value from this door renders zone-less through every type-agnostic path",
					o.name, d.TimeSub)
			}
			if got := d.TimeValue().UTC(); !got.Equal(tstzOriginInstant) {
				t.Fatalf("%s decoded instant %s, want %s", o.name, got, tstzOriginInstant)
			}
			got, err := evalCast(d, "text", 0, tzCtx("ISO, MDY", tstzOriginZone))
			if err != nil {
				t.Fatalf("evalCast(text): %v", err)
			}
			if got.StringValue() != tstzOriginWantText {
				t.Errorf("%s: col::text = %q, want %q (PG 18.3 under TimeZone=%s)",
					o.name, got.StringValue(), tstzOriginWantText, tstzOriginZone)
			}
		})
	}
}

// TestTimestampTZOriginsTagSiblingTypesCorrectly is the other half of the same
// claim, and the reason the fix is a SPLIT of each arm rather than a blanket
// tag: `timestamp without time zone` and `date` share the decode arm with
// timestamptz at every one of these doors, so tagging the arm wholesale would
// make `ts::text` print a zone the value does not have — the mirror-image
// defect. Non-vacuity for that direction: this fails if a fix widens
// `isTimestamptzType` into `isTimestampType`.
//
// It also pins the SECOND divergence this audit found, which was not in the
// ledger row that prompted the slice. `date` has had a behavioural subtype since
// M0097-0063 — TimeSubDate, which Datum.Format() renders date-only and which
// `date + integer` dispatches on — and the heap decode sets it (codec.go's
// "date" arm, NewDateDatum), but none of these four doors did: a date that
// arrived by COPY or came back from an index-only scan printed
// "2020-01-01 00:00:00" where the identical date read from the heap printed
// "2020-01-01". Same audit, same fix, same rule (#2).
func TestTimestampTZOriginsTagSiblingTypesCorrectly(t *testing.T) {
	micros := pgMicrosOf(tstzOriginInstant)
	binTS := make([]byte, 8)
	binary.BigEndian.PutUint64(binTS, uint64(micros))
	binDate := make([]byte, 4)
	binary.BigEndian.PutUint32(binDate, uint32(micros/(24*3600*1_000_000)))

	cases := []struct {
		name string
		d    Datum
	}{
		{"binary COPY timestamp", mustDatum(t, func() (Datum, error) {
			return copyBinaryToDatum(catalog.Type{Name: "timestamp"}, binTS)
		})},
		{"binary COPY date", mustDatum(t, func() (Datum, error) {
			return copyBinaryToDatum(catalog.Type{Name: "date"}, binDate)
		})},
		{"text COPY timestamp", mustDatum(t, func() (Datum, error) {
			return copyTextToDatum(catalog.Type{Name: "timestamp"}, []byte("2020-01-01 04:30:00"), "")
		})},
		{"text COPY date", mustDatum(t, func() (Datum, error) {
			return copyTextToDatum(catalog.Type{Name: "date"}, []byte("2020-01-01"), "")
		})},
		{"index-key timestamp", mustDatum(t, func() (Datum, error) {
			d, err := decodeBTreeKeyToDatum(btree.EncodeTimestamp(micros),
				catalog.Column{Name: "c", Type: catalog.Type{Name: "timestamp"}})
			return d, err
		})},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if c.d.IsTimestampTZ() {
				t.Fatalf("%s tagged a zone-less value as timestamptz", c.name)
			}
			wantDate := c.name == "binary COPY date" || c.name == "text COPY date"
			if c.d.IsDate() != wantDate {
				t.Fatalf("%s: IsDate()=%v, want %v — the door must assign the same "+
					"subtype the heap decode does", c.name, c.d.IsDate(), wantDate)
			}
			got, err := evalCast(c.d, "text", 0, tzCtx("ISO, MDY", tstzOriginZone))
			if err != nil {
				t.Fatalf("evalCast(text): %v", err)
			}
			// timestamp/date print their stored wall clock, unshifted and
			// zone-less, whatever the session TimeZone is.
			want := "2020-01-01 04:30:00"
			if c.name == "binary COPY date" || c.name == "text COPY date" {
				want = "2020-01-01"
			}
			if got.StringValue() != want {
				t.Errorf("%s: ::text = %q, want %q", c.name, got.StringValue(), want)
			}
		})
	}
}

func mustDatum(t *testing.T, f func() (Datum, error)) Datum {
	t.Helper()
	d, err := f()
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	return d
}

// TestPgAuthidValidUntilIsTimestampTZ pins the one non-decoder origin in this
// slice: buildAuthidUserRow declares rolvaliduntil `timestamptz` in its own
// column list two functions above, so the type is a compile-time constant and
// the datum must carry the tag. Left untagged, a `SELECT rolvaliduntil::text
// FROM pg_authid` under a non-UTC session prints a different instant than the
// plain SELECT of the same column.
func TestPgAuthidValidUntilIsTimestampTZ(t *testing.T) {
	row := buildAuthidUserRow(16385, "alice", false, true, false, false, false, false,
		-1, "", "2020-01-01 10:00:00+05:30")

	var idx = -1
	for i, c := range pgAuthidSyncCols() {
		if c.Name == "rolvaliduntil" {
			idx = i
			if c.Type.Name != "timestamptz" {
				t.Fatalf("rolvaliduntil column type is %q — this test's premise is that it is timestamptz", c.Type.Name)
			}
			break
		}
	}
	if idx < 0 {
		t.Fatal("rolvaliduntil not found in pgAuthidSyncCols()")
	}
	d := row[idx]
	if !d.IsTimestampTZ() {
		t.Fatalf("rolvaliduntil datum is untagged (TimeSub=%d)", d.TimeSub)
	}
	got, err := evalCast(d, "text", 0, tzCtx("ISO, MDY", tstzOriginZone))
	if err != nil {
		t.Fatalf("evalCast(text): %v", err)
	}
	if got.StringValue() != tstzOriginWantText {
		t.Errorf("rolvaliduntil::text = %q, want %q", got.StringValue(), tstzOriginWantText)
	}
}
