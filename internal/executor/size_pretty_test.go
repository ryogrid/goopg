package executor

import (
	"testing"
)

func TestSizePretty(t *testing.T) {
	tests := []struct {
		input    int64
		expected string
	}{
		{10, "10 bytes"},
		{1000, "1000 bytes"},
		{1000000, "977 kB"},
		{1000000000, "954 MB"},
		{1000000000000, "931 GB"},
		{1000000000000000, "909 TB"},
		// Boundary tests from postgres/src/test/regress/expected/dbsize.out:
		{10239, "10239 bytes"},
		{10240, "10 kB"},
		{10485247, "10239 kB"},
		{10485248, "10 MB"},
		{10736893951, "10239 MB"},
		{10736893952, "10 GB"},
		{10994579406847, "10239 GB"},
		{10994579406848, "10 TB"},
		{11258449312612351, "10239 TB"},
		{11258449312612352, "10 PB"},
		// Singular byte case
		{1, "1 byte"},
		{0, "0 bytes"},
	}
	for _, tt := range tests {
		got := sizePretty(tt.input)
		if got != tt.expected {
			t.Errorf("sizePretty(%d) = %q, want %q", tt.input, got, tt.expected)
		}
		// Negative of the same value (except 0) should be "-" + positive
		if tt.input > 1 {
			neg := sizePretty(-tt.input)
			want := "-" + tt.expected
			if neg != want {
				t.Errorf("sizePretty(%d) = %q, want %q", -tt.input, neg, want)
			}
		}
	}
}
