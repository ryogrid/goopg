package executor

import (
	"strings"
	"testing"
)

// TestNextvalDetectsInt64Overflow is the review/260831-2 EO2-2 guard.
// seqState.nextVal computed `cur + increment` and compared the result with
// max/min, but int64 addition WRAPS: with a huge increment the sum came back
// negative, sailed past `next > s.max` and was handed out as a sequence value
// (and stored, so the sequence then walked backwards). PG checks for the
// overflow first (sequence.c nextval_internal). Measured on PG 18.3 at
// 127.0.0.1:65438:
//
//	create sequence ovf increment 9223372036854775806 start 3
//	  maxvalue 9223372036854775807;
//	select nextval('ovf');  -> 3
//	select nextval('ovf');  -> ERROR: nextval: reached maximum value of
//	                                  sequence "ovf" (9223372036854775807)
//
// and with CYCLE the second call wraps to the minimum instead:
//
//	select nextval('ovf2'), nextval('ovf2'), nextval('ovf2');
//	  -> 3 | 1 | 9223372036854775807
func TestNextvalDetectsInt64Overflow(t *testing.T) {
	ctx, _, cleanup := newDDLFixture(t)
	defer cleanup()

	if err := runDDL(t, ctx, `CREATE SEQUENCE ovf INCREMENT 9223372036854775806 START 3
	    MAXVALUE 9223372036854775807`); err != nil {
		t.Fatal(err)
	}
	rows, err := runQueryErr(t, ctx, "SELECT nextval('ovf')")
	if err != nil {
		t.Fatalf("first nextval: %v", err)
	}
	if rows[0][0].Int != 3 {
		t.Fatalf("first nextval = %d, want 3", rows[0][0].Int)
	}
	rows, err = runQueryErr(t, ctx, "SELECT nextval('ovf')")
	if err == nil {
		t.Fatalf("second nextval returned %d, want the maximum-value error", rows[0][0].Int)
	}
	if !strings.Contains(err.Error(), "reached maximum value of sequence") {
		t.Errorf("second nextval error = %v, want PG's maximum-value error", err)
	}

	// CYCLE must wrap to the minimum on the same overflow, not emit a
	// negative value.
	if err := runDDL(t, ctx, `CREATE SEQUENCE ovf2 INCREMENT 9223372036854775806 START 3
	    MAXVALUE 9223372036854775807 CYCLE`); err != nil {
		t.Fatal(err)
	}
	want := []int64{3, 1, 9223372036854775807}
	for i, w := range want {
		rows, err := runQueryErr(t, ctx, "SELECT nextval('ovf2')")
		if err != nil {
			t.Fatalf("cycle nextval %d: %v", i+1, err)
		}
		if rows[0][0].Int != w {
			t.Errorf("cycle nextval %d = %d, want %d", i+1, rows[0][0].Int, w)
		}
	}
}
