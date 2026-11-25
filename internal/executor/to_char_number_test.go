package executor

import (
	"errors"
	"os"
	"strconv"
	"strings"
	"testing"

	"github.com/goopg/goopg/internal/utils/misc"
)

// TestToCharMatchesPG18 replays output captured from PostgreSQL 18.3:
// testdata/to_char_pg18.tsv holds the type, value, template and bracketed result
// of to_char for each number type and for intervals (months,days,micros).
func TestToCharMatchesPG18(t *testing.T) {
	raw, err := os.ReadFile("testdata/to_char_pg18.tsv")
	if err != nil {
		t.Fatal(err)
	}
	for _, line := range strings.Split(strings.TrimRight(string(raw), "\n"), "\n") {
		f := strings.Split(line, "\t")
		if len(f) != 4 {
			t.Fatalf("bad line %q", line)
		}
		typeName, value, format := f[0], f[1], f[2]
		want := strings.TrimSuffix(strings.TrimPrefix(f[3], "["), "]")
		var got string
		switch typeName {
		case "interval":
			p := strings.Split(value, ",")
			months, _ := strconv.Atoi(p[0])
			days, _ := strconv.Atoi(p[1])
			micros, _ := strconv.ParseInt(p[2], 10, 64)
			var d Datum
			d, err = toCharInterval(NewIntervalDatumFull(int32(months), int32(days), micros), format)
			if err == nil {
				got = d.StringValue()
			}
		case "int4", "int8":
			n, _ := strconv.ParseInt(value, 10, 64)
			got, err = toCharNumber(NewIntDatum(n), typeName, format)
		default:
			got, err = toCharNumber(NewStringDatum(value), typeName, format)
		}
		if err != nil || got != want {
			t.Errorf("to_char(%s::%s, %q) = %q, %v; want %q", value, typeName, format, got, err, want)
		}
	}
}

func TestToCharTemplateErrors(t *testing.T) {
	for format, want := range map[string]string{
		"9.9.9":   "multiple decimal points",
		"S9S":     `cannot use "S" twice`,
		"9PR9":    `"9" must be ahead of "PR"`,
		"EEEE9":   `"EEEE" must be the last pattern used`,
		"FM9EEEE": `"EEEE" is incompatible with other formats`,
		"RN9S":    `"RN" is incompatible with other formats`,
		"V9.9":    `cannot use "V" and decimal point together`,
	} {
		_, err := toCharNumber(NewIntDatum(1), "int4", format)
		var ee *ExecError
		if !errors.As(err, &ee) || ee.Code != "42601" || ee.Message != want {
			t.Errorf("%q: got %v, want 42601 %q", format, err, want)
		}
	}
	_, err := toCharInterval(NewIntervalDatumFull(1, 0, 0), "Month")
	var ee *ExecError
	if !errors.As(err, &ee) || ee.Code != "22007" || ee.Hint == "" {
		t.Errorf("Month on an interval: got %v", err)
	}
	if _, err := toCharNumber(NewIntDatum(1<<62), "int8", "9V99"); err == nil {
		t.Error("V past the int8 range must fail")
	}
}

func TestToCharNotFiniteInterval(t *testing.T) {
	d := NewIntervalDatumFull(1<<31-1, 1<<31-1, 1<<63-1)
	if got, err := toCharInterval(d, "HH24"); err != nil || !got.IsNull() {
		t.Errorf("infinite interval: %v, %v", got, err)
	}
	if got, err := toCharInterval(NewIntervalDatumFull(0, 0, 0), ""); err != nil || !got.IsNull() {
		t.Errorf("empty template: %v, %v", got, err)
	}
}

// TestToCharUsesTheLocale pins output captured from PostgreSQL 18.3 under a
// de_DE.UTF-8 locale for lc_numeric and lc_monetary.
func TestToCharUsesTheLocale(t *testing.T) {
	de := misc.LocaleConv{DecimalPoint: ",", ThousandsSep: ".", NegativeSign: "-", CurrencySymbol: "€"}
	for _, tc := range []struct{ format, want string }{
		{"L9G999D99", "€ 1.234,50"},
		{"FM9G999D99", "1.234,5"},
		{"S9G999D99", "+1.234,50"},
		{"9,999.99", " 1,234.50"}, // the non-locale patterns do not change
	} {
		got, err := toCharNumberLocale(NewStringDatum("1234.5"), "numeric", tc.format, de)
		if err != nil || got != tc.want {
			t.Errorf("%s: %q, %v; want %q", tc.format, got, err, tc.want)
		}
	}
}
