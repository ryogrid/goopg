package executor

import (
	"strings"
	"testing"
)

// TestBcTrimRightMatchesTrimRight pins bcTrimRight to strings.TrimRight(s,
// " ") on every padding length around the 8-byte stride, with non-blank
// bytes (tab, NUL, multi-byte UTF-8) next to the padding that must stay.
func TestBcTrimRightMatchesTrimRight(t *testing.T) {
	bodies := []string{"", "x", "Electronics", " lead", "a\t", "a\x00", "é", "日本", "        x"}
	for _, b := range bodies {
		for pad := 0; pad <= 41; pad++ {
			for _, tail := range []string{"", "\t", "z"} {
				s := b + strings.Repeat(" ", pad) + tail
				if got, want := bcTrimRight(s), strings.TrimRight(s, " "); got != want {
					t.Fatalf("bcTrimRight(%q) = %q, want %q", s, got, want)
				}
			}
		}
	}
}
