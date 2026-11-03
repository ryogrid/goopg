package executor

import (
	"strings"
	"testing"

	"github.com/goopg/goopg/internal/catalog"
	"github.com/goopg/goopg/internal/optimizer"
)

// combineUserAggState is nodeAgg's combine phase for a CREATE AGGREGATE …
// COMBINEFUNC aggregate (M0148-0001i): the combine state starts from INITCOND
// and a strict COMBINEFUNC follows the strict-transition rules
// (execExprInterp.c EEOP_AGG_PLAIN_TRANS_INIT_STRICT_*).
func iv(d Datum) int64 { n, _ := datumInt64(d); return n }

func TestCombineUserAggStateStrictRules(t *testing.T) {
	ctx := NewContext()
	partial := func(v int64) aggRuntime {
		return aggRuntime{userState: NewIntDatum(v), userStateSet: true, hasValue: true}
	}
	sum := func(t *testing.T, ua *catalog.UserAggregate, parts ...aggRuntime) aggRuntime {
		t.Helper()
		var dst aggRuntime
		call := optimizer.AggregateCall{Name: ua.Name, UserAgg: ua}
		for i := range parts {
			if err := combineAggRuntime(call, &dst, &parts[i], ctx); err != nil {
				t.Fatal(err)
			}
		}
		return dst
	}

	// INITCOND '0', strict int8pl: 0+3+4 = 7.
	withInit := &catalog.UserAggregate{Name: "s", CombineFunc: "int8pl", CombineFuncStrict: true, InitCond: "0"}
	if got := sum(t, withInit, partial(3), partial(4)); got.userState.IsNull() || iv(got.userState) != 7 || !got.hasValue {
		t.Errorf("INITCOND 0: combined state %v (hasValue %v), want 7", got.userState, got.hasValue)
	}

	// No INITCOND, strict: the first partial is adopted (no call with a NULL
	// state), the second is combined in.
	noInit := &catalog.UserAggregate{Name: "s", CombineFunc: "int8pl", CombineFuncStrict: true}
	if got := sum(t, noInit, partial(3), partial(4)); got.userState.IsNull() || iv(got.userState) != 7 {
		t.Errorf("NULL INITCOND: combined state %v, want 7", got.userState)
	}

	// A NULL partial is skipped by a strict COMBINEFUNC.
	nullPart := aggRuntime{userState: NullDatum, userStateSet: true, hasValue: true}
	if got := sum(t, withInit, partial(3), nullPart, partial(4)); got.userState.IsNull() || iv(got.userState) != 7 {
		t.Errorf("NULL partial: combined state %v, want 7", got.userState)
	}

	// A worker that aggregated no rows contributes its INITCOND state.
	if got := sum(t, withInit, aggRuntime{}, partial(5)); got.userState.IsNull() || iv(got.userState) != 5 {
		t.Errorf("empty worker: combined state %v, want 5", got.userState)
	}
}

// The user-aggregate transition state crosses the Gather as a serialized
// column (row transport): the round trip must keep the state Datum and the
// two flags the combine and finalize paths read.
func TestUserAggStateTransportRoundTrip(t *testing.T) {
	call := optimizer.AggregateCall{Name: "s", UserAgg: &catalog.UserAggregate{Name: "s", CombineFunc: "int8pl"}}
	for _, st := range []aggRuntime{
		{userState: NewIntDatum(495000), userStateSet: true, hasValue: true},
		{userState: NullDatum, userStateSet: true, hasValue: true},
		{},
	} {
		d, err := serializeAggCall(call, &st)
		if err != nil {
			t.Fatal(err)
		}
		got, err := deserializeAggCall(call, d.BytesValue())
		if err != nil {
			t.Fatal(err)
		}
		if got.hasValue != st.hasValue || got.userStateSet != st.userStateSet ||
			got.userState.IsNull() != st.userState.IsNull() ||
			(!st.userState.IsNull() && st.userStateSet && iv(got.userState) != iv(st.userState)) {
			t.Errorf("round trip %+v → %+v", st, got)
		}
	}
}

// A serial user aggregate with a builtin SFUNC and a COMBINEFUNC returns the
// aggregate's value. Two defects made it return INITCOND (0 for a 495000 sum,
// PG 18.3 says 495000): the int4_sum SFUNC was missing from the builtin table,
// and the serial finish path called COMBINEFUNC(NULL, state), which PG never
// does outside a split aggregate.
func TestSerialUserAggregateWithBuiltinSFuncAndCombineFunc(t *testing.T) {
	ctx, _, cleanup := newDDLFixture(t)
	t.Cleanup(cleanup)
	for _, q := range []string{
		"create table ua_t (hundred int)",
		"insert into ua_t select g % 100 from generate_series(0, 9999) g",
		"create aggregate ua_sum(int4) (sfunc = int4_sum(int8, int4), stype = int8, combinefunc = int8pl, parallel = safe, initcond = '0')",
		"create function ua_comb(int8, int8) returns int8 strict language sql as 'select $1 + $2'",
		"create aggregate ua_sum2(int4) (sfunc = int4_sum(int8, int4), stype = int8, combinefunc = ua_comb, parallel = safe, initcond = '0')",
	} {
		runSQL(t, ctx, q)
	}
	ps := optimizer.DefaultPlannerSettings()
	got := strings.Join(renderRows(runSQLWith(t, ctx, "select ua_sum(hundred), ua_sum2(hundred), sum(hundred) from ua_t", ps)), ",")
	if got != "495000|495000|495000" {
		t.Errorf("ua_sum, ua_sum2, sum = %q, want 495000|495000|495000 (PG 18.3)", got)
	}
}
