package initdb

import (
	"fmt"
	"strings"
	"testing"
)

// TestPgAmMatchesPostgresBKI pins pg_am (oid amname amhandler amtype).
func TestPgAmMatchesPostgresBKI(t *testing.T) {
	want := map[string]string{}
	for _, f := range bkiInserts(t, "pg_am") {
		want[f[0]] = strings.Join(f, " ")
	}
	got := map[string]string{}
	for _, e := range pgAmInitialEntries() {
		got[fmt.Sprint(e.OID)] = fmt.Sprintf("%d %s %d %c", e.OID, e.Name, e.HandlerID, e.AmType)
	}
	compareBKIRows(t, "pg_am", want, got)
}

// TestPgAmopMatchesPostgresBKI pins pg_amop to PostgreSQL 18.3 row for row
// (oid amopfamily amoplefttype amoprighttype amopstrategy amoppurpose amopopr
// amopmethod amopsortfamily), M0149-0001g slice 8.
func TestPgAmopMatchesPostgresBKI(t *testing.T) {
	want := map[string]string{}
	for _, f := range bkiInserts(t, "pg_amop") {
		want[f[0]] = strings.Join(f, " ")
	}
	got := map[string]string{}
	for _, e := range pgAmopInitialEntries() {
		got[fmt.Sprint(e.OID)] = fmt.Sprintf("%d %d %d %d %d %c %d %d %d", e.OID, e.Family, e.LeftType, e.RightType,
			e.Strategy, e.Purpose, e.Operator, e.Method, e.SortFamily)
	}
	compareBKIRows(t, "pg_amop", want, got)
}

// TestPgAmprocMatchesPostgresBKI pins pg_amproc (oid amprocfamily
// amproclefttype amprocrighttype amprocnum amproc).
func TestPgAmprocMatchesPostgresBKI(t *testing.T) {
	want := map[string]string{}
	for _, f := range bkiInserts(t, "pg_amproc") {
		want[f[0]] = strings.Join(f, " ")
	}
	got := map[string]string{}
	for _, e := range pgAmprocInitialEntries() {
		got[fmt.Sprint(e.OID)] = fmt.Sprintf("%d %d %d %d %d %d", e.OID, e.Family, e.LeftType, e.RightType, e.Num, e.Proc)
	}
	compareBKIRows(t, "pg_amproc", want, got)
}
