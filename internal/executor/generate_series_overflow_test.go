package executor

import (
	"strings"
	"testing"
	"time"
)

// TestGenerateSeriesStepZeroAndOverflow is the review/260831-2 EO2-3 guard,
// covering BOTH generate_series implementations: the SELECT-list expansion in
// operators_project_set.go and the FROM-clause operator in
// operators_generate_series.go.
//
// Two defects, one per implementation detail:
//
//   - `v += step` wraps at the int64 ceiling and the wrapped value is back
//     inside the bounds, so the series never ended: the SELECT-list form
//     appended to a slice until the backend died, the FROM-clause form
//     streamed rows forever. PG ends the iteration on overflow
//     (pg_add_s64_overflow, int8.c).
//   - a zero step yielded an empty series (SELECT-list form) instead of PG's
//     error, and the FROM-clause form reported SQLSTATE 2201F where PG uses
//     22023.
//
// Measured on PG 18.3 at 127.0.0.1:65438:
//
//	select generate_series(9223372036854775805::bigint,
//	                       9223372036854775807::bigint, 3);   -> one row
//	select generate_series(1,3,0);   -> ERROR: step size cannot equal zero
func TestGenerateSeriesStepZeroAndOverflow(t *testing.T) {
	ctx, _, cleanup := newDDLFixture(t)
	defer cleanup()

	const near = "9223372036854775805::bigint, 9223372036854775807::bigint, 3"
	for _, tc := range []struct {
		name string
		sql  string
	}{
		{"select-list", "SELECT generate_series(" + near + ")"},
		{"from-clause", "SELECT x FROM generate_series(" + near + ") x"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			done := make(chan []Row, 1)
			errc := make(chan error, 1)
			go func() {
				rows, err := runQueryErr(t, ctx, tc.sql)
				if err != nil {
					errc <- err
					return
				}
				done <- rows
			}()
			select {
			case rows := <-done:
				if len(rows) != 1 {
					t.Errorf("%s: %d rows, want 1 (the series must stop at the int64 ceiling)", tc.name, len(rows))
				}
			case err := <-errc:
				t.Errorf("%s: %v", tc.name, err)
			case <-time.After(15 * time.Second):
				t.Errorf("%s: no result in 15s — the series is wrapping past the int64 ceiling", tc.name)
			}
		})
	}

	for _, tc := range []struct {
		name string
		sql  string
	}{
		{"select-list", "SELECT generate_series(1, 3, 0)"},
		{"from-clause", "SELECT x FROM generate_series(1, 3, 0) x"},
	} {
		_, err := runQueryErr(t, ctx, tc.sql)
		if err == nil {
			t.Errorf("%s: zero step returned no error", tc.name)
			continue
		}
		if !strings.Contains(err.Error(), "step size cannot equal zero") {
			t.Errorf("%s: error = %v, want PG's zero-step message", tc.name, err)
		}
		if ee, ok := err.(*ExecError); ok && ee.Code != "22023" {
			t.Errorf("%s: SQLSTATE %s, want 22023", tc.name, ee.Code)
		}
	}
}
