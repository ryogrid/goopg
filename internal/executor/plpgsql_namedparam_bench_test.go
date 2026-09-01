package executor

import "testing"

// BenchmarkRewriteSQLNamedParams guards review/260831 ES-7: the per-argument
// regexp used to be compiled on every routine invocation, so the cost of
// calling a PL/pgSQL function grew with its argument count independently of
// the work the body did. The pattern is cached per argument name now.
func BenchmarkRewriteSQLNamedParams(b *testing.B) {
	body := `SELECT a_id, 'literal a_id stays', b_name FROM t
	          WHERE a_id = a_id AND b_name = b_name AND c_flag`
	args := []string{"a_id", "b_name", "c_flag"}

	b.ReportAllocs()
	for b.Loop() {
		if got := rewriteSQLNamedParams(body, args); got == "" {
			b.Fatal("empty rewrite")
		}
	}
}
