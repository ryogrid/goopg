(idle — nothing in flight)

Last landed: DU-002 slice 153 (loop #119) — SECURITY DEFINER + LEAKPROOF
functions now have asserted pg_dump round-trip coverage. CLEAN POSITIVE
(verified empirically), not a divergence: the parser→catalog.Routine→pg_proc_view
chain for prosecdef/proleakproof was already fully wired (unlike slices 150/151's
parsed-then-dropped clauses). Slices 148–152 only ever drove the hardcoded 'f'
for both columns (which dumpFunc suppresses), so no pg_dump round-trip had
asserted these columns reach dumpFunc. dumpFunc (pg_dump.c:13545/13548) appends
` SECURITY DEFINER` then ` LEAKPROOF` inline after STRICT, before COST.

Test fixture: public.add_five(integer) RETURNS integer LANGUAGE sql SECURITY
DEFINER LEAKPROOF. Asserts signature + one-line `LANGUAGE sql SECURITY DEFINER
LEAKPROOF` / `AS $_$ SELECT $1 + 5 $_$;`.

Files: internal/testport/pgdump_connsetup_test.go (fixture ~1537 + assertion
~2030), docs/design/0110-0001-pg-dump-tap-port.md (slice 153 section),
.ralph/fix_plan.md (loop #119 PROGRESS).
Verified: gofmt OK; go build ./internal/... OK; TestPort_PgDumpConnectionSetup
PASS (2.54s, not skipped); ralph-state-guard consistent (auto-repaired stale
completed marker); pgbench smoke runs on commit.

Next direction (slice 154): a fresh pg_dump catalog-surface gap. Best candidate:
CREATE PROCEDURE (prokind='p') round-trip — exercises dumpFunc's keyword=
"PROCEDURE" branch + the no-RETURNS path (pg_dump.c:13483/13497). goopg's
pg_proc_view already emits prokind='p' (line 326-328) but NO procedure has ever
been dumped, so this likely surfaces a REAL divergence (prorettype handling for
procedures, or getFuncs discovery of prokind='p'). Alternative lower-risk:
a STABLE or PARALLEL RESTRICTED volatility variant (clean-positive coverage).
