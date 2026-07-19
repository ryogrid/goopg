(idle — nothing in flight)

Last loop (#40): M0123-S4 sub-slice 26 — canonical `date` (OID 1082) Const datums.
LANDED + committed. A `date` column DEFAULT literal (`d date DEFAULT '2024-03-15'`)
now folds to a by-value DateADT Const (int32 days-since-2000, constlen 4, consttype
1082) byte-for-byte identical to PG18.3's pg_attrdef.adbin. date_in is TimeZone-
INDEPENDENT so (unlike timestamptz) any plain ISO date folds deterministically; the
only guard is calendar validity (j2date∘date2j round-trip rejects month 13 / Feb 30).

Files: internal/pgnodes/datum.go (OidDate=1082, NewDateConst, parseDateDays,
formatDate — reuse existing date2j/j2date/parseDateFields math); resolver_expr.go
(StringConst date arm parallel to timestamptz); rebuild.go (rebuildConst OidDate case);
date_test.go (NEW: 5 live goldens + math table + degradation); oracle_pgnodes_adbin_
test.go (+3 date cases → 64 total); executor/sys_pg_attrdef_test.go (+date-lit / date-
lit-invalid). NO executor change (TypeNameToOID("date")==1082 already routes it).
Design 0123-0005 §"Sub-slice 26" + fix_plan + ledger.

Gates GREEN: full pgnodes pkg; adbin oracle 64/64 byte-identical vs LIVE PG18.3;
executor+initdb attrdef siblings; go build ./..., go vet (pgnodes/testport/executor),
gofmt clean; pgbench smoke via pre-commit.

Nightly triage (run 20260719-094219, sha c217c692): all 5 AI items already [x] stale
(predate HEAD). No new nightly work.

Next (M0123-S4 REMAINING): (1) float4-common (no float8) CASE result mix — int/numeric
→float4 arms + outer float8(float4) column cast (selectCaseCommonType/coerceCaseResult);
(2) date-time-family CASE coercion — now that the date datum exists, needs a `::date`/
`::timestamptz` CastExpr fold of a StringConst in a natural/non-column context; (3) other
length types (varchar(N)=CoerceViaIO, timestamp(N), bit(N)); (4) operator-driven view-qual
coercion; (5) broader date input forms — infinity/-infinity, BC years, DateStyle-dependent
MDY/DMY, textual month (ledger sub-slice 26 resume point). ALL numeric + date column
DEFAULT Const shapes now canonical.
