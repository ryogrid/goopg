package parser

import "testing"

// BenchmarkParseTake3 covers the pgbench statement mix.
//
// It exists because perf-optimize-take3/05 §3 found the tree had NO benchmark
// that measures yyParserImpl.Parse on a non-empty grammar: PERF-BASELINE.md's
// rows gate the parser-rewrite wave flips and its "new parser" numbers are an
// empty-input floor. This is the allocation regression guard for the pooled
// parser (parser_pool.go) — B/op is the signal, not just ns/op.
func BenchmarkParsePGBenchMix(b *testing.B) {
	cases := []struct{ name, sql string }{
		{"BEGIN", "BEGIN"},
		{"SelectAbalance", "SELECT abalance FROM pgbench_accounts WHERE aid = 8215525"},
		{"UpdateAccounts", "UPDATE pgbench_accounts SET abalance = abalance + 42 WHERE aid = 8215525"},
		{"InsertHistory", "INSERT INTO pgbench_history (tid, bid, aid, delta, mtime) VALUES (7, 1, 8215525, 42, CURRENT_TIMESTAMP)"},
	}
	for _, c := range cases {
		b.Run(c.name, func(b *testing.B) {
			b.ReportAllocs()
			b.ResetTimer()
			for range b.N {
				if _, err := Parse(c.sql); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}
