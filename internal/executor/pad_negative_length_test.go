package executor

import (
	"testing"

	"github.com/goopg/goopg/internal/optimizer"
)

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

// TestRepeatNegativeCount verifies repeat(text, int) clamps a negative count
// to 0, matching PG's text_repeat (postgres/src/backend/utils/adt/varlena.c),
// instead of panicking on strings.Repeat with a negative count. M0134-0070.
func TestRepeatNegativeCount(t *testing.T) {
	cases := []struct {
		name string
		s    string
		n    int64
		want string
	}{
		{"negative count", "Pg", -4, ""},
		{"zero count", "Pg", 0, ""},
		{"positive count, non-regression pin", "Pg", 3, "PgPgPg"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			fc := &optimizer.FuncCall{
				Name: "repeat",
				Args: []optimizer.Expr{
					&optimizer.StringConst{Value: c.s},
					&optimizer.IntegerConst{Value: c.n},
				},
			}
			got, err := evalFuncCall(fc, nil, &Context{})
			if err != nil {
				t.Fatalf("evalFuncCall: %v", err)
			}
			if got.StringValue() != c.want {
				t.Errorf("repeat(%q, %d) = %q, want %q", c.s, c.n, got.StringValue(), c.want)
			}
		})
	}
}
