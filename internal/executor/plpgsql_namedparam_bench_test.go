package executor

import (
	"fmt"
	"strings"
	"testing"
)

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

// BenchmarkTriggerFiring measures a row-level PL/pgSQL trigger firing per
// inserted row (review/260831 ES-9): the trigger body used to be re-parsed on
// every firing.
func BenchmarkTriggerFiring(b *testing.B) {
	ctx, cleanup := newVMFixture(b)
	defer cleanup()
	run := func(sql string) { benchExecSQL(b, ctx, sql) }

	run("CREATE TABLE trigbench (id int, v int)")
	run(`CREATE FUNCTION trigbench_fn() RETURNS trigger AS $$
BEGIN
  NEW.v := NEW.v + 1;
  RETURN NEW;
END;
$$ LANGUAGE plpgsql`)
	run("CREATE TRIGGER trigbench_trg BEFORE INSERT ON trigbench FOR EACH ROW EXECUTE FUNCTION trigbench_fn()")

	var vals strings.Builder
	for i := 0; i < 100; i++ {
		if i > 0 {
			vals.WriteString(",")
		}
		fmt.Fprintf(&vals, "(%d, %d)", i, i)
	}
	stmt := "INSERT INTO trigbench VALUES " + vals.String()

	b.ReportAllocs()
	for b.Loop() {
		run(stmt)
	}
}
