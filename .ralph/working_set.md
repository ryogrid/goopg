Task just completed: M0134-0109 (create_am.sql) — sized live against PG
18.3 oracle: PARKED (case genuinely `failed`, 0% parity).

Landed one contained fix:
`CREATE ACCESS METHOD ... HANDLER <builtin>` (gisthandler, heap_tableam_handler,
bthandler, hashhandler, ginhandler, spghandler, brinhandler) raised a false
42883 "function does not exist" even though all 7 are seeded pg_proc rows —
`resolveAccessMethodHandlerFunc` (internal/executor/operators_ddl.go) only
searched `catalog.Routines()` (the CREATE FUNCTION registry); goopg has no
pluggable-storage-engine registry so built-ins were never in it. Added a
small leaf-package `builtinAMHandlerFuncs` name->{OID,AMType} table as a
fallback (same import-cycle-driven duplication pattern as
pg_proc_names_generated.go). Verified all 6 positive/negative
handler-resolution lines byte-for-byte against create_am.out.

CSV row (create_am.sql) flipped not-tried -> failed via `make regen-testport`
(genuinely failed, stays not-pass-required). Design doc
docs/design/m0134-0109-create-am-handler-resolution.md, README.md indexed.
fix_plan.md M0134-0109 marked [x] PARKED. Ledger row appended
(.ralph/deferral_ledger.md, M0134-0109): CREATE TABLE/MATERIALIZED VIEW
... USING <am> has ZERO parser support at all (syntax error), plus
ALTER TABLE/MATVIEW SET ACCESS METHOD, default_table_access_method
enforcement, partitioned-table AM inheritance, AM pg_depend tracking, and
a real opclass over the excluded GiST AM — multi-milestone storage-
pluggability feature, correctly out of contained-fix scope.

NEXT LOOP: per the Current Priority banner in .ralph/fix_plan.md, continue
M0134 top-to-bottom — next unworked item is **M0134-0110**.

Standing recommendation (carried across several loops, still open):
1. brin_summarize_range/brin_desummarize_range unimplemented, blocks 3 files
   (M0134-0095/-0096/-0097 PARKs).
2. A collation-execution-registry gap recurs across FIVE parked files
   (M0134-0099/-0100/-0101/-0102).
3. bucket (5) from M0134-0102: internal/executor/expr.go length/upper/lower/
   etc. swallow a nested function-not-found error into NULL instead of
   propagating 42883 (systemic, cross-file).
4. The ctid/tableoid system-column pattern (13-file wiring, M0134-0104) is a
   template a future loop could generalize to cmin/cmax/xmin/xmax.
5. LANGUAGE C dynamic-extension-loading gap (M0134-0106) — no C-extension
   loader exists anywhere in goopg.
6. EUC_JP/UTF8 (and likely other EUC_*/SJIS/BIG5/GBK pairs) real Unicode
   mapping tables are unported (~11k lines of upstream .map data,
   M0134-0107) — structural-only validators are landed but genuine
   non-ASCII round-trip conversion is not.
7. `CREATE TABLE ... USING <am>` (table-AM selection clause) has zero
   parser support anywhere (NEW this loop, M0134-0109) — a future
   table/index-AM-pluggability milestone would need: parser grammar for
   USING on CREATE TABLE/CREATE TABLE AS/CREATE MATERIALIZED VIEW,
   ALTER TABLE/MATVIEW SET ACCESS METHOD as a new AlterTable subcommand,
   default_table_access_method GUC enforcement, and relam/pg_depend
   plumbing at table-creation time.

Gates run: go build ./... clean; go test ./internal/catalog/...
./internal/executor/... PASS; RALPH_PRECOMMIT_SCOPE=units
scripts/ralph-precommit-test.sh PASS (all unit packages, ~440s cold
internal/initdb run — toolchain/branch state, not a regression); make
regen-testport clean (had to fix a stray comma in my own CSV rationale
text that broke the field count — caught by check-testport-inventory);
make check-testport-inventory PASS; make ralph-state-guard PASS
(auto-repaired the same benign stale status/progress running-vs-completed
mismatch seen in prior loops, then confirmed consistent). Pre-commit
hook's pgbench smoke will run automatically at commit time (mandatory,
never bypassed).

In-flight: none. Throwaway test server (/tmp/amtest-data, port 5533,
GOOPG_CG_UNIT=amtest) was stopped cleanly via `goopg stop` before commit —
verified no orphan process remains (only a shell-snapshot subprocess
matched the grep, not a goopg server).
