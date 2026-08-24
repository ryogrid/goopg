Task just completed: M0134-0128 (hash_func.sql) — sized live, PARKED, two
contained fixes shipped.

`scripts/pg-regress-runner.sh hash_func`: 0% parity, diff 447→317 lines
(374-line expected output). Unusual shape among M0134 cases: every query is
a self-consistency check (`WHERE hash != extended0 OR hash == extended1`
expecting 0 rows) — no fixed hash values pinned, so any internally-consistent
extended/non-extended pair passes regardless of exact bit-for-bit PG match
(though this implementation does match, since it reuses hash_partition.go's
already-PG-faithful primitives).

Two independent contained fixes landed:
1. `integer::bit(n)` cast had ZERO support (blocked every statement in the
   file) — `5::bit(32)` silently stringified to "5" instead of PG's 32-digit
   binary string. Added `intToBitTypmodString` (internal/executor/expr.go,
   mirrors PG's bitfromint4/bitfromint8: low-N-bits copy when typmod fits the
   source width, sign-extension otherwise) wired into the CastExpr eval site
   ahead of evalCastTyped (bit width lives in x.Typmod, which
   evalCastTyped's two-string signature has no slot for).
2. hashint2/4/8/oid/char/float4/float8/name/text/bpchar + their *extended
   siblings were pg_proc-seeded but had ZERO scalar-call dispatch (42883
   even for hashint8, which already has a Go impl only reachable via
   LANGUAGE INTERNAL routine bodies, plpgsql_runtime.go). Added
   evalHashFunc (internal/executor/expr.go) wired from 10 new case labels in
   evalFuncCall's switch, reusing hash_partition.go's Jenkins-hash primitives
   (pgHashUint32Extended/pgHashBytesExtended/pgHashInt8, M0097-0027 +
   M0134-0071) — pure wiring, no new algorithm work. Added one new primitive
   pgHashUint32 (hash_partition.go).

New tests: TestHashFuncScalarFamily (13 subtests), TestIntToBitTypmodCast
(internal/executor/hash_func_scalar_test.go).

Design `docs/design/m0134-0128-hash-func-scalar-family.md`, indexed in
README.md. Ledger row: `.ralph/deferral_ledger.md` 2026-08-24 M0134-0128.
CSV flipped `not-tried` → `failed` via `make regen-testport`. fix_plan.md
M0134-0128 marked [x] with full summary.

Deferred (18 more hash-function families, each needs type-specific
canonical-byte encoding or a general type→hash-proc dispatch mechanism):
hashmacaddr/hashmacaddr8/hashinet, hash_numeric (needs PG's base-10000
digit/weight extraction — goopg's numeric Datum isn't stored that way),
hash_array/hash_record/hash_range/hash_multirange (need a generic runtime
type→hash-proc dispatch — only precedent is LookupOpClassHashFunc, opclass-
scoped only), hashoidvector, hash_aclitem, hashenum (needs the enum label's
assigned OID — goopg's KindEnum Datum only carries {SortOrder,Label}),
time_hash/timetz_hash/interval_hash/timestamp_hash, uuid_hash, pg_lsn_hash,
jsonb_hash. Also noted in passing (NOT this task's scope): `-'NaN'::float4`/
`-'NaN'::float8` raises a spurious "operator unary - requires integer or
numeric" — pre-existing unrelated evaluator bug surfaced by this file's
float special-case probes.

NEXT LOOP: per the Current Priority banner, continue M0134 top-to-bottom —
next unworked item is **M0134-0129 — indirect_toast.sql**. Size it live
first per the established pattern (run pg-regress-runner, read the diff,
check whether the root cause is a shared/already-tracked blocker before
assuming fresh work).

Standing recommendation, carried across several loops (unchanged this loop):
1. **GIN/GiST/SPGiST physical-index plan integration** — confirmed across
   THREE files (gin.sql M0134-0126, create_index_spgist.sql M0134-0111,
   gist.sql M0134-0127) — every predicate on any of these three index AMs
   EXPLAINs Seq Scan not Index/Index-Only Scan because the AM is
   catalog-only. Strongest candidate for a dedicated milestone.
2. Geometry type-system gap (point/lseg/line/path/polygon typed-literal
   parsing + operator lexer family) — box.sql/circle.sql/geometry.sql/
   gist.sql shared blocker, resume points in
   `docs/design/m0134-0125-geometry-sizing.md`.
3. LANGUAGE C dynamic-extension loading gap — recurs across M0134-0106,
   -0116, -0120, create_operator/create_type adjacent files.
4. Collation-execution-registry gap recurs across FIVE parked files
   (M0134-0099/-0100/-0101/-0102).
5. BETWEEN-vs-comparison-operator precedence bug (M0134-0113) — silently
   wrong for ANY query today, cross-cutting.
6. RAISE INFO/LOG/DEBUG collapse to hardcoded NOTICE wire severity
   (M0134-0113, ~18 call sites in plpgsql_runtime.go).
7. `::json` cast DETAIL/CONTEXT truncation text (json_errdetail port) —
   M0134-0120, unfixed.
8. pg_shdepend-shaped object-enumeration/CASCADE engine — CONFIRMED A
   THIRD TIME (M0134-0124), after M0134-0117/-0118. Single most-recurring
   blocker across M0134; strong candidate for its own milestone. Resume:
   `catalog.InMemory.RoleDropDependencyDescriptions`
   (`internal/catalog/catalog.go`).
9. `CREATE CONVERSION`-registered procs never consulted by convert_from/
   convert_to (M0122-0008, M0134-0121).
10. DDL-event-trigger firing engine + `session_replication_role` GUC
    (M0134-0122/-0123) — second-most-recurring blocker.
11. `NonSuperuserRole != ""` "is superuser" convention is wrong for any
    non-"postgres"-named `CREATE ROLE ... SUPERUSER` role — worth a
    dedicated sweep.

Gates run this loop: scripts/pg-regress-runner.sh hash_func (sizing run, 0/1,
before and after the fix — 447→317 diff lines); go build ./... PASS; go test
./internal/executor/... ./internal/parser/... PASS (includes 2 new test
funcs, 15 subtests); RALPH_PRECOMMIT_SCOPE=units scripts/ralph-precommit-test.sh
PASS (all packages, cold internal/initdb 435s + cmd/goopg 77s, rest cached);
scripts/tpch-spotcheck.sh PASS (Q12=2 rows 21.9s, Q13=35 rows 8.1s, 31.8s
query-phase wall); make check-testport-inventory PASS; make regen-testport
PASS; make ralph-state-guard: found the same benign stale clean-exit-marker
status/progress mismatch as prior loops, auto-repaired to progress=in_progress;
pre-commit hook's pgbench smoke will run automatically at commit time.

In-flight: none.

Note: a concurrent peer session's WIP was present in the tree again this
loop (.ralph/progress.json, .ralphrc, analysis/*, ci/logs/launch.log,
ci/logs/scheduler.log, docs/wiki/*,
internal/executor/operators_recursive_cte.go, postgres (untracked convenience
symlink), third-party/tpcds-postgres, plus new untracked files
analysis/deferral-ledger-summary-20260824/, dl_summary_session.txt) and was
deliberately left untouched/uncommitted — only this loop's own files were
staged and committed by explicit pathspec.

M-NIGHTLY: not re-checked this loop (working_set.md is written after the
main task; nightly triage happens at loop start per the standing rule — no
new ci/logs/action-items.md run observed to differ from the prior loop's
already-filed 20260824-013441 run during this loop's brief look at the
directory).
