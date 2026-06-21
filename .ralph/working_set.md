Task: M0118-0003 (row locking) — COMPLETE this loop: promoted `tuplelock-update`
(slice 12). The M0118-0003 spec group continues (more specs below).

DONE this loop (committed):
- internal/planner/planner.go: new shared helper `defaultMarkerReplacement(tbl,
  ordinal)` — returns the column's catalog DefaultExpr if present, ELSE a
  synthesized `nextval('<table>_<col>_seq')` *parser.FuncCall for
  catalog.IsSerialTypeName / IdentityColumn columns, ELSE *parser.NullConst.
  Wired into all 3 DEFAULT-marker substitution sites (rewriteInsertDefaultMarkers
  row cell; rewriteUpdateDefaultMarkers single-column + tuple-form). Fixes
  explicit `DEFAULT` on a SERIAL column collapsing to NULL (23502).
- internal/testport/framework/isolation_runner.go: `hasPendingStepCompleteBlocker`
  + `reportStepGatedOnBlockers` — the immediate-completion branch now honours
  BlockerStepComplete step-name annotations (e.g. `s1_advunlock1(s2_update)`),
  rendering the unlock step in <waiting ...>/<... completed> format gated behind
  the referenced pending blocker step.
- internal/planner/planner_test.go: +TestPlanInsert/UpdateValuesDefaultSerial
  SubstitutesNextval (pin the nextval rewrite).
- internal/testport/isolation_port_test.go: +TestPort_IsolationTuplelockUpdate.
- CSV target-inventory: tuplelock-update failed→pass (comma-free rationale);
  regenerated postgres-oracle-target-inventory.md + upstream-isolation-coverage.md
  (isolation pass 72→73).
- docs/design/0118-0002-*: slice 12 section + status checklist (✅ tuplelock-update)
  + README index slice-12 sentence. Ledger row appended.

ROOT CAUSE: tuplelock-update setup `INSERT INTO pktab VALUES (1, DEFAULT)` where
`data` is `SERIAL NOT NULL`. goopg leaves catalog DefaultExpr nil for serial cols
(the executor nextval auto-gen loop is authoritative only for OMITTED columns),
so the DEFAULT marker → NullConst → column counted as explicitly-provided-NULL →
auto-gen skipped → NOT NULL 23502. Second divergence: runner ignored
BlockerStepComplete annotations. The ROW-LOCK ENGINE was already correct (slices
5/6 forward lock propagation handle FOR KEY SHARE + chained no-key updaters).

GATES (all PASS): go build ./...; go vet planner+testport; gofmt clean;
full internal/planner + internal/parser + internal/executor suites PASS;
16 TestPort_Isolation lock specs (LockUpdateDelete/Traversal, Nowait/2/3/4/5,
SkipLocked/2/3/4, TuplelockConflict, LockCommitted{Update,Keyupdate},
UpdateLockedTuple, TuplelockUpdate) all PASS no silent skips; ralph-state-guard
OK (auto-repaired progress→in_progress); pgbench smoke via pre-commit hook.
DO NOT stage: postgres, weekly_loc.*, requirements.txt, weekkly_loc_history.py.

>>> NEXT STEP (continue M0118-0003 row-locking group, one spec per loop):
    RESUME at `tuplelock-partition` (INSERT ON CONFLICT UPDATE routing on a
    LIST-partitioned table — 2 perms: no-key UPDATE proceeds vs s2 FOR KEY SHARE;
    key-UPDATE blocks s2 until s1c) OR `lock-nowait` (LOCK TABLE — needs a
    txn-scoped heavyweight lock lifecycle, [[lockmgr_locks_are_statement_scoped]])
    OR `propagate-lock-delete` (FK-INSERT lock propagation + RI trigger).
    Per-spec workflow: add TestPort_Isolation<Name> for the live diff → fix →
    green → CSV failed→pass (rationale=Go func, COMMA-FREE) → regen
    gen-isolation-coverage + gen-oracle-inventory → design doc slice + README +
    ledger.

GOTCHAS: CSV rationale MUST be comma-free (unquoted comma-delimited rows) —
[[serena_replace_content_dotall_eats_file]]; prefer built-in Edit for Go code.
The BlockerStepComplete runner support added this loop also unblocks output
ordering for deadlock-hard / detach-partition-concurrently-3/4 /
intra-grant-inplace-db / partition-drop-index-locking when later ported (only 6
upstream specs use parenthesised step blockers). tpch-spotcheck INFRA-BLOCKED
(SLRU backfill >60s); row-lock path never fires in pgbench TPC-B/TPC-H so TPS
blast radius nil; the planner DEFAULT-on-serial change is guarded by full
planner/executor suites + pgbench pre-commit smoke.
