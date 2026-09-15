Task: M0142-0003g — index-accelerate + interrupt-check goopg's FK constraint
validation scan (blocker filed by -0003f). **DONE and committed this loop.**

Files: `internal/executor/operators_fk.go` (production fix — see below).
`.ralph/fix_plan.md` (-0003g marked `[x]` with landing summary; new
`M0142-0003i` filed `[ ]` to resume -0003f). `docs/design/0100-0149/
m0142-0003g-fk-index-accelerated-validation.md` (new). `docs/design/README.md`
(indexed).

Key symbols (new/changed in operators_fk.go): `scanRelForFKMatch` (now a
2-branch dispatcher), `findFKCoveringUniqueIndex`, `fkProbeKeyForIndex`,
`scanIndexForFKMatch`, `fkPendingOutcome` (shared visibility/multixact logic,
extracted verbatim), `scanRelForFKMatchSeq` (renamed old full-scan fallback),
`fullTableFKCheckRel` (added per-block `ctx.Ctx.Err()` check).

Findings this loop: (1) The fix routes the parent-existence probe through the
parent's covering unique btree index (`BTree.RangeScan(key,key,...)`) instead
of a full heap scan, dropping the cost from O(child × parent-scan) to
O(child × log parent) — matches PG's own indexed RI-trigger approach.
(2) **First attempt had a real bug, caught by the test suite, not by me**:
built the probe key via `encodeIndexKeyFromCols` directly (the "blob" key
format) instead of `ctx.indexRowProbeKey` (which picks between the blob
format and PG's real index-tuple format via `ctx.pgIndexKeyDesc`) — this
silently mismatched the actual on-disk key shape and produced false
"not found" results. `TestAlterTableAddForeignKeyDanglingRow` and
`NotValidThenValidate` failed with the WRONG value reported as missing
(reported a=1, which exists, instead of the actually-dangling a=5) — go
test ./internal/executor/... is what caught it; fixed by switching to
`ctx.indexRowProbeKey`, same builder `checkUniqueIndexesForInsert` already
uses. Full package green after the fix (12.9s). (3) Verified at realistic
scale (partsupp/part: 800k child / 200k parent) on an isolated throwaway
cluster (port 5533, /tmp/goopg-fk-check, cleaned up): ADD CONSTRAINT FK went
from "did not finish in several minutes" (unfixed) to 6.7s clean / 9.4s with
one dangling row (correct 23503 + byte-exact DETAIL). (4) `scripts/
tpch-spotcheck.sh` SKIPPED — its `pg_basebackup` clone of the shared `:65433`
cluster came up with `lineitem` missing. Diagnosed (not fixed) as likely
caused by the abandoned PID 81 backend still stuck mid-DDL on the *old*
binary (left running since -0003f, see below) corrupting the online clone's
consistency point — confirmed via `git stash` that this is NOT caused by my
diff (an unrelated pre-existing parser test, `TestLockingClauseParity`,
fails identically with/without it). Filed as new evidence for -0003h.

Next step: **M0142-0003i** (filed) — now that -0003g is unblocking, resolve
the PID 81 backend (terminate it or restart the shared `:65433` server onto a
binary that includes this fix — should be safe now since the scan it's stuck
in no longer exists), add the remaining 5 TPC-H FK constraints
(`partsupp_part_fk`, `partsupp_supplier_fk`, `order_customer_fk`,
`lineitem_partsupp_fk`, `lineitem_order_fk`), persist the DDL in
`bench/tpch/build_schema_goopg.sh` so it survives `--reset`, then re-run the
Q9 `EXPLAIN`/estimate-audit capture from -0003a/-0003d (needs
`lineitem_partsupp_fk` specifically) to see whether the row-estimate collapse
clears. **M0142-0003h** (DDL-under-BEGIN transactionality repro) remains open
and independent. Neither is mandated over other M0142/M0141 items by the
banner (still item 4) — other open items unchanged: M0142-0005 (per-worker
Memoize cache recon), M0142-0008a/0008b (SEMI/ANTI decorrelation scoping),
M0142-0016c (Q33/Q54/Q56 shape check), M0141-S2b/S3-S7 (upper-planner
ordering, Incremental Sort).

Gates run: `go build ./...` clean. `go vet ./internal/executor/...` clean.
`gofmt -l internal/executor/operators_fk.go` clean (manually fixed an import
group order the new `nbtree` import disturbed — did NOT run `gofmt -w`
per the go1.25-vs-local-gofmt rule). `go test ./internal/executor/...` full
package PASS (12.9s), including all FK/Unique-named tests individually.
`RALPH_PRECOMMIT_SCOPE=units scripts/ralph-precommit-test.sh`: one failure,
`TestLockingClauseParity` in `internal/parser` — confirmed via `git stash`
this is pre-existing/unrelated (fails identically without this loop's diff;
stale `GroupedJoinUnaliased` AST field, a parser-generation drift). Realistic-
scale functional/perf validation on an isolated throwaway cluster (see
Findings (3) above) substitutes for the SKIPPED `tpch-spotcheck.sh`.
`make ralph-state-guard`: same pre-existing stale progress-marker pattern as
recent loops, self-repaired, passed clean.

In-flight: none. The throwaway `/tmp/goopg-fk-check*` server/binary/data were
stopped and deleted this loop. The shared `:65433` cluster's PID 81 backend
is UNCHANGED by this loop (still stuck on the old binary, confirmed at loop
start: 11 rows in `pg_constraint`, PID 81 `active` running the same
`partsupp_part_fk` statement) — left as-is deliberately again, now explicitly
handed to **M0142-0003i** as its first sub-step rather than re-deferred
silently.
