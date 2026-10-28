package executor

// bcTrimRight returns s without its trailing ASCII spaces: the image PG's
// bcTruelen (varchar.c) measures, which bpchar comparison, hashing and
// grouping all work on. It is strings.TrimRight(s, " ") — same result — but
// drops whole 8-byte words of blanks before finishing byte by byte: a
// char(n) value carries up to n blanks of padding (TPC-DS's char(50)
// category/class/brand columns ~40), and the byte loop over them was 6.5 %
// of TPC-DS Q67's instructions (M0122-0012 C25). The 8-byte compare against
// a constant compiles to one load and one compare.
func bcTrimRight(s string) string {
	n := len(s)
	for n >= 8 && s[n-8:n] == "        " {
		n -= 8
	}
	for n > 0 && s[n-1] == ' ' {
		n--
	}
	return s[:n]
}
