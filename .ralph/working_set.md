Task just completed: M0134-0111 (create_index_spgist.sql) — sized live
against PG 18.3 oracle: PARKED (case genuinely `failed`, diff 1576→1516
lines / 0% parity).

Landed two contained fixes:
1. `point <@ box` / `box @> point` containment (internal/executor/expr.go,
   the `parser.OpContainedBy`/`OpContains`/`OpOverlap` arm from M0097-0023)
   only ever attempted box-vs-box parsing (`parseBoxText` both sides) — a
   bare point operand always hard-errored "invalid box value", even in the
   file's pure-seqscan section that needs no index. Fixed via a
   `parsePointText` fallback (treat point as degenerate box, equal corners
   — existing containment formulas need no further branching).
2. `starts_with(text, text)` (pg_proc oid 3696) — catalog-registered
   (isKnownBuiltinFunction) but zero `evalFuncCall` dispatch arm; added
   beside the existing `left`/`right` cases.

Both covered by new unit tests: internal/executor/geometry_containment_test.go
(TestEvalPointBoxContainment), internal/executor/starts_with_test.go
(TestEvalStartsWith).

Design docs/design/m0134-0111-create-index-spgist-sizing.md, README.md
indexed. fix_plan.md M0134-0111 marked [x] PARKED. Ledger row appended
(.ralph/deferral_ledger.md, M0134-0111): two independent multi-file gaps
left the case at 0% parity —
(a) the operator lexer (internal/parser/lexer.go:548-575) only recognizes a
    hardcoded 2/3-char operator whitelist, not PG's real graphic-operator-
    char grammar (scan.l), so `<<|`, `|>>`, `~=`, `~<~`, `~<=~`, `~>=~`,
    `~>~`, `^@` all fail as syntax errors — SYSTEMIC, not SP-GiST-specific;
    likely affects other already-parked/not-tried M0134 cases too (not yet
    swept).
(b) SP-GiST (internal/executor/amutils.go, operators_ddl.go:7537-7543) is
    catalog-metadata-only, same class as GiST/BRIN — zero physical
    quad-tree/kd-tree/radix-tree storage anywhere, so every
    indexscan/bitmapscan EXPLAIN plan in the file diverges to Seq Scan.

NEXT LOOP: per the Current Priority banner in .ralph/fix_plan.md, continue
M0134 top-to-bottom — next unworked item is **M0134-0112
(create_misc.sql)**.

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
6. EUC_JP/UTF8 real Unicode mapping tables unported (M0134-0107).
7. `CREATE TABLE ... USING <am>` has zero parser support (M0134-0109).
8. `::` cast evaluator never consults pg_cast for user-defined casts
   (M0134-0110).
9. NEW this loop (M0134-0111): the operator-lexer whitelist gap
   (internal/parser/lexer.go:548-575, hardcoded 2/3-char switch instead of
   PG's general graphic-operator-char grammar) is itself a template gap
   worth a future dedicated milestone — likely blocks other not-yet-sized
   M0134 cases that use non-whitelisted operator spellings (not spot-checked
   against other parked/not-tried rows yet).

Gates run: go build ./... clean; go test ./internal/executor/...
./internal/parser/... PASS (new TestEvalPointBoxContainment,
TestEvalStartsWith green); RALPH_PRECOMMIT_SCOPE=units
scripts/ralph-precommit-test.sh PASS (all unit packages, ~458s cold
internal/initdb run — toolchain/branch state, not a regression); make
regen-testport clean; make check-testport-inventory PASS; make
ralph-state-guard PASS (auto-repaired the same benign stale
running-vs-completed status/progress mismatch seen in prior loops, then
confirmed consistent). Pre-commit hook's pgbench smoke runs automatically at
commit time — mandatory, never bypassed.

In-flight: none. No throwaway test servers were started this loop (sizing
used scripts/pg-regress-runner.sh's own managed throwaway cluster, which
cleans itself up).
