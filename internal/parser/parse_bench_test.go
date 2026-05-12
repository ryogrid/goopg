package parser

import "testing"

// BenchmarkParseUpdate measures Parse throughput for a pgbench UPDATE query.
// M0098-0006: validates pool-based allocation reduction.
func BenchmarkParseUpdate(b *testing.B) {
	query := "UPDATE pgbench_accounts SET abalance = abalance + $1 WHERE aid = $2"
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		stmts, err := Parse(query)
		if err != nil {
			b.Fatal(err)
		}
		_ = stmts
	}
}

func BenchmarkParseSelect(b *testing.B) {
	query := "SELECT abalance FROM pgbench_accounts WHERE aid = $1"
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		stmts, err := Parse(query)
		if err != nil {
			b.Fatal(err)
		}
		_ = stmts
	}
}

// TestParsePoolConcurrency verifies that concurrent Parse calls using the
// pool don't corrupt each other's token slices. M0098-0006.
func TestParsePoolConcurrency(t *testing.T) {
	queries := []string{
		"SELECT 1",
		"UPDATE pgbench_accounts SET abalance = abalance + $1 WHERE aid = $2",
		"SELECT abalance FROM pgbench_accounts WHERE aid = $1",
		"INSERT INTO pgbench_history (tid, bid, aid, delta, mtime) VALUES ($1, $2, $3, $4, CURRENT_TIMESTAMP)",
		"BEGIN",
		"COMMIT",
	}
	done := make(chan struct{})
	for i := 0; i < 8; i++ {
		go func(goroutine int) {
			defer func() { done <- struct{}{} }()
			for iter := 0; iter < 500; iter++ {
				q := queries[iter%len(queries)]
				stmts, err := Parse(q)
				if err != nil {
					t.Errorf("goroutine %d iter %d: Parse(%q): %v", goroutine, iter, q, err)
					return
				}
				if len(stmts) != 1 {
					t.Errorf("goroutine %d iter %d: Parse(%q): got %d stmts, want 1", goroutine, iter, q, len(stmts))
					return
				}
			}
		}(i)
	}
	for i := 0; i < 8; i++ {
		<-done
	}
}
