package executor

import "testing"

// TestPadLeftNegativeLength verifies padLeft clamps a negative target length
// to 0, matching PG's text_lpad (postgres/src/backend/utils/adt/varlena.c),
// instead of panicking on runes[:n] with n < 0. M0134-0070.
func TestPadLeftNegativeLength(t *testing.T) {
	cases := []struct {
		name string
		s    string
		n    int
		fill string
		want string
	}{
		{"negative length, explicit fill", "hi", -5, "xy", ""},
		{"negative length, default fill", "hi", -5, "", ""},
		{"positive length, non-regression pin", "hi", 5, "xy", "xyxhi"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := padLeft(c.s, c.n, c.fill)
			if got != c.want {
				t.Errorf("padLeft(%q, %d, %q) = %q, want %q", c.s, c.n, c.fill, got, c.want)
			}
		})
	}
}

// TestPadRightNegativeLength verifies padRight clamps a negative target
// length to 0, matching PG's text_rpad (postgres/src/backend/utils/adt/varlena.c),
// instead of panicking on runes[:n] with n < 0. M0134-0070.
func TestPadRightNegativeLength(t *testing.T) {
	cases := []struct {
		name string
		s    string
		n    int
		fill string
		want string
	}{
		{"negative length, explicit fill", "hi", -5, "xy", ""},
		{"negative length, default fill", "hi", -5, "", ""},
		{"positive length, non-regression pin", "hi", 5, "xy", "hixyx"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := padRight(c.s, c.n, c.fill)
			if got != c.want {
				t.Errorf("padRight(%q, %d, %q) = %q, want %q", c.s, c.n, c.fill, got, c.want)
			}
		})
	}
}
