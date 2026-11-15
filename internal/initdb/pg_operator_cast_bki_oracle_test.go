package initdb

import (
	"fmt"
	"strings"
	"testing"
)

// bkiRegproc maps a postgres.bki regproc field to its OID text: bki writes
// InvalidOid as "-" (genbki's representation of a zero regproc).
func bkiRegproc(f string) string {
	if f == "-" {
		return "0"
	}
	return f
}

func boolChar(b bool) string {
	if b {
		return "t"
	}
	return "f"
}

// TestPgOperatorMatchesPostgresBKI pins goopg's bootstrap pg_operator rows to
// PostgreSQL 18.3's postgres.bki row for row (M0149-0001g slice 9): oid oprname
// oprnamespace oprowner oprkind oprcanmerge oprcanhash oprleft oprright
// oprresult oprcom oprnegate oprcode oprrest oprjoin. A PG node resolves
// operators, commutators, negators and selectivity estimators from these rows.
func TestPgOperatorMatchesPostgresBKI(t *testing.T) {
	want := map[string]string{}
	for _, f := range bkiInserts(t, "pg_operator") {
		f[1] = strings.Trim(f[1], "'")
		for i := 12; i <= 14; i++ {
			f[i] = bkiRegproc(f[i])
		}
		want[f[0]] = strings.Join(f, " ")
	}
	got := map[string]string{}
	for _, e := range pgOperatorInitialEntries() {
		got[fmt.Sprint(e.OID)] = fmt.Sprintf("%d %s %d %d %c %s %s %d %d %d %d %d %d %d %d",
			e.OID, e.Name, e.Namespace, e.Owner, e.Kind, boolChar(e.CanMerge), boolChar(e.CanHash),
			e.LeftType, e.RightType, e.ResultType, e.Commutator, e.Negator, e.Code, e.Restrict, e.Join)
	}
	compareBKIRows(t, "pg_operator", want, got)
}

// TestPgCastMatchesPostgresBKI pins pg_cast (oid castsource casttarget
// castfunc castcontext castmethod).
func TestPgCastMatchesPostgresBKI(t *testing.T) {
	want := map[string]string{}
	for _, f := range bkiInserts(t, "pg_cast") {
		want[f[0]] = strings.Join(f, " ")
	}
	got := map[string]string{}
	for _, e := range pgCastInitialEntries() {
		got[fmt.Sprint(e.OID)] = fmt.Sprintf("%d %d %d %d %c %c", e.OID, e.CastSource, e.CastTarget, e.CastFunc, e.CastContext, e.CastMethod)
	}
	compareBKIRows(t, "pg_cast", want, got)
}
