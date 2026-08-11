package executor

import (
	"errors"
	"testing"
	"time"

	"github.com/goopg/goopg/internal/pgdatetime"
)

// TestParsePGDateTextTextualMonth pins the DATE entry point (the typed-literal
// and cast arms) on the textual month forms PostgreSQL decodes independently of
// the DateOrder GUC — 'MON-DD-YYYY', 'DD-MON-YYYY' and 'YYYY-MON-DD', upstream's
// own list in DecodeNumber. Every expectation was probed against PG 18.3
// (reference cluster, port 65432, `DateStyle = ISO, MDY`).
//
// Before this, `'May 1, 2002'::date` and `'1-Jan-2020'::date` matched no entry
// in the layout table and came back as 22007 — which for the COMPARISON path
// (tryParseStringAs) is not even an error but a silent "leave it a string",
// i.e. the M0125-0007 wrong-answer shape.
func TestParsePGDateTextTextualMonth(t *testing.T) {
	may1 := time.Date(2002, 5, 1, 0, 0, 0, 0, time.UTC)
	cases := []struct {
		lit  string
		want time.Time
	}{
		{"2002-May-1", may1},
		{"May-1-2002", may1},
		{"1-May-2002", may1},
		{"May 1, 2002", may1},
		{"1 May 2002", may1},
		{"2002 May 1", may1},
		{"1/May/2002", may1},
		{"1-may-2002", may1},
		{"1-MAY-2002", may1},
		{"1-September-2002", time.Date(2002, 9, 1, 0, 0, 0, 0, time.UTC)},
		{"sept 1 2002", time.Date(2002, 9, 1, 0, 0, 0, 0, time.UTC)},
		{"1-May-69", time.Date(2069, 5, 1, 0, 0, 0, 0, time.UTC)},
		{"1-May-70", time.Date(1970, 5, 1, 0, 0, 0, 0, time.UTC)},
		{"02-May-1", time.Date(2001, 5, 2, 0, 0, 0, 0, time.UTC)},
	}
	// The era suffix suppresses the 2-digit-year window, exactly as it does for
	// the run-together numeric form ('1-May-02 BC' is 0002-05-01 BC to PG, not
	// 2002-05-01 BC). That is asserted one layer down, in pgdatetime's
	// TestNormalizeDateTimeInputTextualMonth: goopg's KindTime carrier is a
	// nanosecond count, so no BC date survives checkTimeCarrierRange here
	// regardless of how it was spelled (ledger: the ns->microsecond carrier
	// move).
	for _, c := range cases {
		got, err := parsePGDateText(c.lit)
		if err != nil {
			t.Errorf("parsePGDateText(%q) = error %v, want %v", c.lit, err, c.want)
			continue
		}
		if !got.UTC().Equal(c.want) {
			t.Errorf("parsePGDateText(%q) = %v, want %v", c.lit, got.UTC(), c.want)
		}
	}
}

// TestParsePGDateTextTextualMonthOutOfRange pins the SQLSTATE split PG makes
// once the shape IS recognised: '2002-Feb-30' is a field-range error (22008),
// not a syntax one (22007), because DecodeDate decoded it fine and only
// ValidateDate objected. The textual month has to reach the same
// validateDateTokenFull battery the numeric spellings do.
func TestParsePGDateTextTextualMonthOutOfRange(t *testing.T) {
	for _, lit := range []string{"2002-Feb-30", "Feb 30, 2002", "31-Apr-2002"} {
		_, err := parsePGDateText(lit)
		if !errors.Is(err, pgdatetime.ErrFieldOutOfRange) {
			t.Errorf("parsePGDateText(%q) = %v, want ErrFieldOutOfRange (PG raises 22008)", lit, err)
		}
	}
}

// TestParsePGDateTextTextualMonthRejects keeps the widening honest: a spelling
// PostgreSQL rejects must keep failing here rather than be silently invented.
// 'septem' is not a datetktbl entry (there is no prefix rule — 'sept' is its own
// row), '10:00 May' is a time-of-day plus a month with no date to build, and
// the ISO 'T' is not a field break after a textual month (PG: "invalid input
// syntax for type timestamp").
func TestParsePGDateTextTextualMonthRejects(t *testing.T) {
	for _, lit := range []string{"septem 1 2002", "10:00 May", "May 2002", "May Jun 2002", "-May-1-2002"} {
		if got, err := parsePGDateText(lit); err == nil {
			t.Errorf("parsePGDateText(%q) = %v, want an error (PG rejects it)", lit, got)
		}
	}
	if got, err := parsePGTimestampText("2002-May-1T10:20:30"); err == nil {
		t.Errorf("parsePGTimestampText(%q) = %v, want an error (PG rejects the 'T' here)", "2002-May-1T10:20:30", got)
	}
}

// TestParsePGTimestampTextTextualMonth is the timestamp/timestamptz sibling:
// the date half is normalised the same way and the time and zone halves keep
// going through the shared layout table.
func TestParsePGTimestampTextTextualMonth(t *testing.T) {
	cases := []struct {
		lit  string
		zone tsZoneMode
		want time.Time
	}{
		{"2002-May-1 10:20:30", tsDiscardZone, time.Date(2002, 5, 1, 10, 20, 30, 0, time.UTC)},
		{"May 1, 2002 10:20", tsDiscardZone, time.Date(2002, 5, 1, 10, 20, 0, 0, time.UTC)},
		// tsApplyZone moves the wall clock onto the UTC line: 10:20:30.5+05:30
		// is 04:50:30.5Z.
		{"May 1, 2002 10:20:30.5+05:30", tsApplyZone,
			time.Date(2002, 5, 1, 4, 50, 30, 500000000, time.UTC)},
		{"1-May-2002 10:00 z", tsApplyZone, time.Date(2002, 5, 1, 10, 0, 0, 0, time.UTC)},
	}
	for _, c := range cases {
		got, err := parsePGTimestampTextZone(c.lit, c.zone)
		if err != nil {
			t.Errorf("parsePGTimestampTextZone(%q) = error %v, want %v", c.lit, err, c.want)
			continue
		}
		if !got.UTC().Equal(c.want) {
			t.Errorf("parsePGTimestampTextZone(%q) = %v, want %v", c.lit, got.UTC(), c.want)
		}
	}
}

// TestTryParseStringAsTextualMonthDate is the comparison-path guard, the one
// that turns a rejected spelling into a WRONG ANSWER rather than an error:
// promoteCrossKind reports a failed coercion as "leave it a string", so
// `d_date = 'May 1, 2002'` used to match zero rows in silence.
func TestTryParseStringAsTextualMonthDate(t *testing.T) {
	want := time.Date(2002, 5, 1, 0, 0, 0, 0, time.UTC)
	for _, lit := range []string{"May 1, 2002", "1-May-2002", "2002-May-1"} {
		got := tryParseStringAs(KindTime, lit)
		if got.Kind != KindTime {
			t.Errorf("tryParseStringAs(KindTime, %q) left it a %v — the comparison would silently be false", lit, got.Kind)
			continue
		}
		if !got.TimeValue().UTC().Equal(want) {
			t.Errorf("tryParseStringAs(KindTime, %q) = %v, want %v", lit, got.TimeValue().UTC(), want)
		}
	}
}
