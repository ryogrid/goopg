package tapnode

import "testing"

// TestPGStyleLogLine pins how goopg's structured log lines render in PG's
// stderr form for ported log assertions.
func TestPGStyleLogLine(t *testing.T) {
	cases := map[string]string{
		`time=2026-11-25T00:00:00.000+09:00 level=WARN msg="recovery_end_command \"x\": child process exited with exit code 1"`:                    `WARNING:  recovery_end_command "x": child process exited with exit code 1`,
		`time=2026-11-25T00:00:00.000+09:00 level=INFO msg="restored log file \"00000002.history\" from archive"`:                                  `LOG:  restored log file "00000002.history" from archive`,
		`time=2026-11-25T00:00:00.000+09:00 level=INFO msg=hello a=b`:                                                                              `LOG:  hello a=b`,
		`2026/11/25 00:00:00 WARN archive gone`:                                                                                                    `WARNING:  archive gone`,
		`time=2026-11-25T00:00:00.000+09:00 level=ERROR+4 msg="WAL was generated with \"wal_level=minimal\", cannot continue recovering" detail=x`: `FATAL:  WAL was generated with "wal_level=minimal", cannot continue recovering detail=x`,
		`plain text`: `plain text`,
		// The log_line_prefix attribute renders ahead of the level, and the
		// goopg-only proto attribute is dropped, so the statement text ends
		// the line as it does in PG (014_binary: "… TO STDOUT\n").
		`time=2026-11-26T00:00:00.000+09:00 level=INFO msg="statement: COPY public.t TO STDOUT" prefix="2026-11-26 00:00:00.000 JST [3] " proto=simple`: `2026-11-26 00:00:00.000 JST [3] LOG:  statement: COPY public.t TO STDOUT`,
	}
	for in, want := range cases {
		if got := pgStyleLogLine(in); got != want {
			t.Errorf("pgStyleLogLine(%q)\n got %q\nwant %q", in, got, want)
		}
	}
}
