Task just completed: M0134-0106 (conversion.sql) — sized live against PG 18.3
oracle: **PARKED** (case genuinely `failed`, 613-line diff / 0% parity, three
contained validation/catalog fixes shipped).

Landed three independent catalog-registry bugs:
1. `CREATE DEFAULT CONVERSION` had no encoding-pair uniqueness check — ported
   `FindDefaultConversion` (postgres/src/backend/catalog/pg_conversion.c:66-79)
   into `catalog.InMemory.CreateConversion` (internal/catalog/catalog.go): a
   second default conversion for the same (namespace, for-encoding,
   to-encoding) triple now raises "default conversion for %s to %s already
   exists", independent of name (previously silently accepted regardless).
2. `COMMENT ON CONVERSION` had NO case at all in execCommentOn's switch
   (internal/executor/operators_ddl.go) — silently no-op'd for both existing
   and nonexistent conversion names. Added a case resolving via
   im.FindConversion, storing under pgConversionRelOID (2607); raises 42704
   on a nonexistent name.
3. `DROP CONVERSION` on a real conversion fell through past a successful
   im.DropConversion() call (missing `return nil`) into a DropCompatObject
   gate keyed by a MISMATCHED name spelling (schema-qualified at CREATE time
   "public.mydef" vs bare at DROP time "mydef") — the mismatch made a
   genuinely successful drop raise a false "does not exist". Fixed by adding
   the early return, mirroring the sibling text-search-dictionary/
   -configuration branches in the same switch that already did this right.

613 -> 602 diff lines, 2 -> 1 `^-ERROR`. Design doc
docs/design/m0134-0106-conversion-catalog-fixes.md, README.md indexed.
Ledger row appended (.ralph/deferral_ledger.md, 2026-08-24, M0134-0106) — the
dominant remaining bucket (~85% of diff) is test_conv(), a table-valued
wrapper around two LANGUAGE C harness functions (test_enc_setup,
test_enc_conversion) that goopg can neither parse (CREATE FUNCTION requires
explicit RETURNS; PG derives RECORD from OUT params) nor execute (no
C-extension dynamic-loading engine exists anywhere in goopg) — REFACTOR-tier,
no contained slice available. CSV row flipped not-tried -> failed via
`make regen-testport`. fix_plan.md M0134-0106 marked [x].

Committed and pushed to origin/regress-renumbering — see git log for the
commit sha (committed at end of this loop).

Nightly filing: checked ci/logs/action-items.md at loop start — same run
(20260824-013441, sha e7495e712dda) as prior 12 loops, both AI- items already
filed (fix_plan.md lines ~1286/~1312: AdvisoryLock repeat regression + units/
internal/executor regression). No new filing needed this loop.

NEXT LOOP: per the Current Priority banner in .ralph/fix_plan.md, continue
M0134 top-to-bottom — next unworked item is **M0134-0107 (copyencoding.sql)**.
Standing recommendation (carried across several loops, still open, still not
selected because the banner's straight top-to-bottom order wins absent
explicit re-prioritization):
1. brin_summarize_range/brin_desummarize_range unimplemented, blocks 3 files
   (M0134-0095/-0096/-0097 PARKs).
2. A collation-execution-registry gap recurs across FIVE parked files
   (M0134-0099/-0100/-0101/-0102, ICU/libc/builtin providers all share "no
   locale-aware comparator wired into comparison/sort") — a strong unifying
   candidate if a loop gets reassigned to infra work.
3. bucket (5) from M0134-0102: internal/executor/expr.go length/upper/lower/
   octet_length/etc. swallow a nested function-not-found error into NULL
   instead of propagating 42883 (systemic, cross-file, needs its own
   verification pass across the whole regress-port suite before editing).
4. The ctid/tableoid system-column pattern (CTIDExpr, 13-file wiring, from
   M0134-0104) is a template a future loop could generalize to
   cmin/cmax/xmin/xmax — the MVCC storage side is already 100% done/verified.
5. NEW this loop: a LANGUAGE C dynamic-extension-loading gap (M0134-0106) —
   no C-extension loader exists anywhere in goopg; only exposed once so far
   but likely to recur across the remaining regress files that use PG's own
   `regress.so` test-harness functions.

Gates run: go build ./... clean; RALPH_PRECOMMIT_SCOPE=units
scripts/ralph-precommit-test.sh PASS (all unit packages, cache mostly warm);
make regen-testport clean; make check-testport-inventory PASS; make
ralph-state-guard PASS (auto-repaired the same benign stale status/progress
running-vs-completed mismatch seen in prior loops, then confirmed
consistent). Pre-commit hook's pgbench smoke runs automatically at commit
time (mandatory, never bypassed).

In-flight: none. No throwaway server was left running — pg-regress-runner.sh
manages its own goopg instance and stopped cleanly after each invocation.
