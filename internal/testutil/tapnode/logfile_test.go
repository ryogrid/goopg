package tapnode

import "testing"

// TestPGStyleLogLine pins how goopg's structured log lines render in PG's
// stderr form for ported log assertions.
func TestPGStyleLogLine(t *testing.T) {
	cases := map[string]string{
		`time=2026-11-25T00:00:00.000+09:00 level=WARN msg="recovery_end_command \"x\": child process exited with exit code 1"`: `WARNING:  recovery_end_command "x": child process exited with exit code 1`,
		`time=2026-11-25T00:00:00.000+09:00 level=INFO msg="restored log file \"00000002.history\" from archive"`:               `LOG:  restored log file "00000002.history" from archive`,
		`time=2026-11-25T00:00:00.000+09:00 level=INFO msg=hello a=b`:                                                           `LOG:  hello a=b`,
		`2026/11/25 00:00:00 WARN archive gone`:                                                                                 `WARNING:  archive gone`,
		`plain text`:                                                                                                            `plain text`,
	}
	for in, want := range cases {
		if got := pgStyleLogLine(in); got != want {
			t.Errorf("pgStyleLogLine(%q)\n got %q\nwant %q", in, got, want)
		}
	}
}
