package catalog

// D-09: AttAlignPointer unit table — the shared peek rule. Non-zero
// first byte (short header or aligned multi-byte word): no align.
// Zero (pad or unaligned 4B low byte): align. OOB: unchanged (caller
// owns bounds). Non-varlena: unconditional.
import "testing"

func TestAttAlignPointer(t *testing.T) {
	data := []byte{0x00, 0x07, 0x00, 0x00, 0x40, 0x00, 0x09, 0x00}
	tests := []struct {
		name      string
		off       int
		align     int
		isVarlena bool
		want      int
	}{
		{"nonvarlena aligns", 1, 4, false, 4},
		{"nonvarlena aligned noop", 4, 4, false, 4},
		{"nonvarlena align1 noop", 3, 1, false, 3},
		{"varlena nonzero stays", 1, 4, true, 1},
		{"varlena zero aligns 4", 0, 4, true, 0},
		{"varlena zero aligns 8", 5, 8, true, 8},
		{"varlena zero aligns 2", 3, 2, true, 4},
		{"varlena nonzero at 6 stays", 6, 8, true, 6},
		{"oob unchanged", 99, 4, true, 99},
		{"oob at len unchanged", 8, 4, true, 8},
		{"empty data unchanged", 0, 4, true, 0},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			var d []byte
			if tc.name != "empty data unchanged" {
				d = data
			}
			if got := AttAlignPointer(d, tc.off, tc.align, tc.isVarlena); got != tc.want {
				t.Errorf("AttAlignPointer(off=%d, align=%d, varlena=%v) = %d, want %d",
					tc.off, tc.align, tc.isVarlena, got, tc.want)
			}
		})
	}
}
