package misc

import (
	"testing"
	"time"
)

// TestFormatTimestampFractionalSeconds matches PostgreSQL's AppendSeconds
// (postgres/src/backend/utils/adt/datetime.c): a zero fsec omits the
// decimal point entirely, and a non-zero fsec is trimmed of trailing zeros
// rather than always padded to 6 digits.
func TestFormatTimestampFractionalSeconds(t *testing.T) {
	base := time.Date(2026, time.July, 14, 9, 5, 3, 0, time.UTC)
	cases := []struct {
		name string
		ns   int
		want string
	}{
		{"zero fsec omits dot", 0, "2026-07-14 09:05:03"},
		{"half second", 500_000_000, "2026-07-14 09:05:03.5"},
		{"trailing zeros trimmed", 120_000_000, "2026-07-14 09:05:03.12"},
		{"full microsecond precision", 123_456_000, "2026-07-14 09:05:03.123456"},
	}
	for _, tc := range cases {
		when := time.Date(base.Year(), base.Month(), base.Day(), base.Hour(), base.Minute(), base.Second(), tc.ns, time.UTC)
		got := FormatTimestamp(when, "ISO", "MDY")
		if got != tc.want {
			t.Errorf("%s: FormatTimestamp() = %q, want %q", tc.name, got, tc.want)
		}
	}
}

// TestDateStylePartialSetPreservesOrder mirrors PostgreSQL's
// check_datestyle behavior (postgres/src/backend/commands/variable.c):
// a SET naming only the style must keep the session's current order
// component rather than resetting it to the boot default.
func TestDateStylePartialSetPreservesOrder(t *testing.T) {
	r := BuildDefaultRegistry()
	sess := NewSessionRegistry(r)

	if err := sess.Set("datestyle", "DMY", false); err != nil {
		t.Fatal(err)
	}
	if _, eff, _ := sess.Get("datestyle"); eff != "ISO, DMY" {
		t.Errorf("after SET datestyle=DMY: got %q, want %q", eff, "ISO, DMY")
	}

	// A subsequent style-only SET must keep the DMY order just set above,
	// not silently reset it back to the boot default's MDY.
	if err := sess.Set("datestyle", "SQL", false); err != nil {
		t.Fatal(err)
	}
	if _, eff, _ := sess.Get("datestyle"); eff != "SQL, DMY" {
		t.Errorf("after SET datestyle=SQL: got %q, want %q", eff, "SQL, DMY")
	}
}

// TestDateStyleGermanImpliesDMY matches upstream: GERMAN also sets DMY
// order unless the same SET explicitly names an order.
func TestDateStyleGermanImpliesDMY(t *testing.T) {
	r := BuildDefaultRegistry()
	sess := NewSessionRegistry(r)

	if err := sess.Set("datestyle", "German", false); err != nil {
		t.Fatal(err)
	}
	if _, eff, _ := sess.Get("datestyle"); eff != "German, DMY" {
		t.Errorf("after SET datestyle=German: got %q, want %q", eff, "German, DMY")
	}

	if err := sess.Set("datestyle", "German, YMD", false); err != nil {
		t.Fatal(err)
	}
	if _, eff, _ := sess.Get("datestyle"); eff != "German, YMD" {
		t.Errorf("after SET datestyle='German, YMD': got %q, want %q", eff, "German, YMD")
	}
}

// TestDateStyleConflictingSpecRejected matches upstream's "Conflicting
// \"DateStyle\" specifications" rejection for two style (or two order)
// keywords in the same SET.
func TestDateStyleConflictingSpecRejected(t *testing.T) {
	r := BuildDefaultRegistry()
	sess := NewSessionRegistry(r)

	if err := sess.Set("datestyle", "ISO, SQL", false); err == nil {
		t.Error("expected error for conflicting styles ISO, SQL")
	}
	if err := sess.Set("datestyle", "MDY, DMY", false); err == nil {
		t.Error("expected error for conflicting orders MDY, DMY")
	}
}

// TestDateStyleUnrecognizedKeywordRejected matches upstream's
// "Unrecognized key word" rejection.
func TestDateStyleUnrecognizedKeywordRejected(t *testing.T) {
	r := BuildDefaultRegistry()
	sess := NewSessionRegistry(r)

	if err := sess.Set("datestyle", "bogus", false); err == nil {
		t.Error("expected error for unrecognized datestyle keyword")
	}
}

// TestDateStyleDefaultTokenMergesBootValue matches upstream's recursive
// DEFAULT handling: "DEFAULT, ISO" takes the boot order (MDY) but
// overrides the style to ISO.
func TestDateStyleDefaultTokenMergesBootValue(t *testing.T) {
	r := BuildDefaultRegistry()
	sess := NewSessionRegistry(r)

	if err := sess.Set("datestyle", "German, YMD", false); err != nil {
		t.Fatal(err)
	}
	if err := sess.Set("datestyle", "DEFAULT, SQL", false); err != nil {
		t.Fatal(err)
	}
	if _, eff, _ := sess.Get("datestyle"); eff != "SQL, MDY" {
		t.Errorf("after SET datestyle='DEFAULT, SQL': got %q, want %q", eff, "SQL, MDY")
	}
}

// TestDateStyleBootValueRoundTrips confirms the registry boot value
// ("ISO, MDY") canonicalizes to itself and SHOW reflects it before any
// SET, matching PostgreSQL's compiled-in default.
func TestDateStyleBootValueRoundTrips(t *testing.T) {
	r := BuildDefaultRegistry()
	sess := NewSessionRegistry(r)
	if _, eff, _ := sess.Get("datestyle"); eff != "ISO, MDY" {
		t.Errorf("boot datestyle = %q, want %q", eff, "ISO, MDY")
	}
}
