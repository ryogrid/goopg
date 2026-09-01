package executor

import (
	"strings"
	"testing"
	"time"
)

// TestPLpgSQLForStepMustBePositive is the review/260831-2 ES-6 guard.
// The integer FOR loop took its BY value unvalidated, so `BY 0` (and any
// negative step) reached `for i := l; i <= u; i += stepVal`, which never
// advances past the bound: the backend spun forever inside the function,
// unkillable except by cancelling the session. PG rejects the step before
// the loop starts (pl_exec.c), measured on PG 18.3 at 127.0.0.1:65438:
//
//	do $$ begin for i in 1..5 by 0 loop end loop; end $$;
//	ERROR:  BY value of FOR loop must be greater than zero
//
//	do $$ begin for i in 1..5 by -1 loop end loop; end $$;
//	ERROR:  BY value of FOR loop must be greater than zero
func TestPLpgSQLForStepMustBePositive(t *testing.T) {
	ctx, _, cleanup := newDDLFixture(t)
	defer cleanup()

	for _, stmt := range []string{
		`CREATE FUNCTION for_zero_step() RETURNS int LANGUAGE plpgsql AS $$
		   DECLARE n int := 0;
		   BEGIN
		     FOR i IN 1..5 BY 0 LOOP n := n + 1; END LOOP;
		     RETURN n;
		   END $$`,
		`CREATE FUNCTION for_neg_step() RETURNS int LANGUAGE plpgsql AS $$
		   DECLARE n int := 0;
		   BEGIN
		     FOR i IN 1..5 BY -1 LOOP n := n + 1; END LOOP;
		     RETURN n;
		   END $$`,
	} {
		if err := runDDL(t, ctx, stmt); err != nil {
			t.Fatalf("fixture statement failed: %v\nSQL: %s", err, stmt)
		}
	}

	for _, fn := range []string{"for_zero_step", "for_neg_step"} {
		// Run off-goroutine: before the fix this call never returns, and a
		// hung guard test is indistinguishable from a slow one.
		type result struct{ err error }
		done := make(chan result, 1)
		go func() {
			_, err := runQueryErr(t, ctx, "SELECT "+fn+"()")
			done <- result{err}
		}()
		select {
		case r := <-done:
			if r.err == nil {
				t.Errorf("SELECT %s() returned no error, want the BY-value error", fn)
				continue
			}
			if !strings.Contains(r.err.Error(), "BY value of FOR loop must be greater than zero") {
				t.Errorf("SELECT %s() error = %v, want PG's BY-value message", fn, r.err)
			}
		case <-time.After(10 * time.Second):
			t.Fatalf("SELECT %s() did not return within 10s — the FOR loop is spinning", fn)
		}
	}
}
