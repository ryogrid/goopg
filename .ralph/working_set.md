(idle — nothing in flight)

Last landed: DU-002 slice 148 (loop #113) — `CREATE FUNCTION` now round-trips
through pg_dump byte-identically. Slice 147 created public.add_one(integer) only
as a COMMENT target and asserted just the comment; the CREATE FUNCTION body was
emitted but never asserted — and carried a real defect.
THE BUG: goopg's *virtual* pg_proc view typed `prosupport` as `oid`, emitting
text "0". pg_dump's dumpFunc (pg_dump.c:13575) emits `SUPPORT <val>` whenever
`strcmp(prosupport,"-") != 0`, so the dump carried `LANGUAGE sql SUPPORT 0` —
invalid DDL (SUPPORT wants a function name; restore would fail). Real PG types
prosupport `regproc`, which renders InvalidOid as "-".
FIX: pg_proc_view.go — retype prosupport column oid→regproc; emit cell "-"
(not "0") in BOTH row builders. TypedVirtualCell parses "-" as non-int →
StringConst("-") → wire text "-" → dumpFunc suppresses the clause.
NOTE: `$_$ SELECT $1 + 1 $_$` quoting is CORRECT (pg_dump escalates the dollar
tag when the body has a bare `$`, here `$1`), not a bug. The physical heap
pg_proc bootstrap already typed prosupport regproc w/ binary 0 — only the
virtual view diverged.
Key symbols: registerPgProcView, TypedVirtualCell (planner.go:2296),
dumpFunc (pg_dump.c), catalog.Type regproc.
Files: internal/initdb/pg_proc_view.go (col type + 2 cells + doc),
internal/initdb/pg_proc_view_test.go (TestPgProcViewProsupport: now pins
type=regproc, value="-"), internal/testport/pgdump_connsetup_test.go (slice 148
assertions: exact LANGUAGE/AS fragment + negative SUPPORT 0 guard),
docs/design/0110-0001-pg-dump-tap-port.md (Slice 148 section).
Verified: gofmt OK; go build ./internal/initdb OK; initdb suite PASS (116s);
catalog/planner PASS; executor Proc/Func/Comment PASS;
TestPort_PgDumpConnectionSetup PASS (2.98s). ralph-state-guard OK.

Next direction (slice 149): a fresh pg_dump catalog-surface gap. Candidates:
assert ALTER FUNCTION ... OWNER TO round-trips; a 2nd function with different
volatility/strict/SECURITY DEFINER to exercise those dumpFunc clauses; a
set-returning function (ROWS clause); or a procedure (CREATE PROCEDURE / prokind
'p'). Lesson from this slice: a virtual catalog column's *type* drives its wire
text (regproc 0 → "0" vs "-"); pg_dump compares against PG's regproc sentinels.
