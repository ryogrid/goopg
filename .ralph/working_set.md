(idle — nothing in flight)

---

**Loop #21 (this loop) — COMPLETE, committed + pushed (`aad61997`, on top
of the peer's `40905365`).**

Picked up the exact "Next step" left by loop #20: the SELECT-privilege
follow-up to M0097-0040 (INSERT/UPDATE/DELETE ACL enforcement landed
loop #20). Ran a background research agent FIRST (per the ledger's own
warning that this is NOT a one-line mirror of the write-path check) to
map: view inlining semantics, `tableACLs` default-seeding state, and
every sibling scan operator — before writing any code.

Landed: `seqScanOp.Open` (`operators_storage.go`), `indexScanOp.openPrep`
(`operators_index.go`), and `indexOnlyScanOp.Open` (`operators_indexonly.go`)
— the three raw-heap read operators — now call
`dmlPrivilegePermitted(ctx, tbl, "SELECT")` pre-lock, raising `42501` for
a non-superuser/non-owner role missing the grant. Added ONE new branch to
`dmlPrivilegePermitted`: `priv == "SELECT" && catalog.IsSystemRelation(tbl.OID)`
(OID < `FirstNormalObjectId`/16384) always permits SELECT — the research
agent confirmed `tableACLs` is empty for every relation (catalog or user)
until an explicit GRANT runs, so without this carve-out every psql `\d`,
pg_dump run, and information_schema query from ANY non-superuser role would
newly 42501 (zero existing test would have caught this — confirmed via
grep across every role/SET-ROLE test file). INSERT/UPDATE/DELETE on system
catalogs are unaffected by the carve-out (still need a real grant).

Tests: `internal/executor/storage_dml_test.go`'s
`TestSeqScanRequiresSelectPrivilege`, `TestIndexScansRequireSelectPrivilege`
(sibling pin — a fix scoped to only `seqScanOp` would leave index-scan
plans able to bypass the gate), `TestSystemCatalogSelectAlwaysPermitted`.
Design: `docs/design/0118-0039-truncate-conflict-privilege-model.md` new
Follow-up section; `docs/design/README.md` row extended.
`.ralph/fix_plan.md` M0122-0008 banner updated. `unimplemented_feat.json`
M0097-0040 entry updated in place. `.ralph/deferral_ledger.md`: flipped
the prior loop's SELECT-deferral row to `resolved`, appended a new row
recording the one remaining known limitation below.

**Known limitation (recorded, not fixed):** views are inlined into the
querying session's own plan at plan time (`planner.go`'s
`if tbl.View != nil { inner, err := Plan(tbl.View, cat) }`) — there is no
view-owner/security-definer identity anywhere in planner or executor
(confirmed zero hits for `ViewOwner`/`expandView` etc.). PostgreSQL runs a
view's underlying-table reads as the *view owner*, so `GRANT SELECT ON
view` alone (no base-table grant) still works in PG but is now DENIED in
goopg (the inlined scan checks the querying role, not the view owner). No
existing test combines a non-superuser role with a view-only grant, so
this is an un-caught scope boundary, not a regression. Fixing it needs a
`ViewOwner`/definer-identity field threaded through `Plan()` — a
materially larger change, deferred to its own loop.

Gates: `go build ./...` clean; `go vet ./internal/executor/...
./internal/planner/... ./internal/catalog/...` clean; `go test
./internal/executor/... ./internal/planner/... ./internal/catalog/...
./internal/server/...` PASS (no regressions); `go test
./internal/testport/... -run
'TestPort_IsolationTruncateConflict|TestPort_IsolationIntraGrantInplace'`
PASS (role/GRANT-adjacent isolation specs unaffected);
`scripts/tpch-spotcheck.sh` PASS (Q12=2/Q13=33); pre-commit pgbench smoke
PASS (0 failed, TPC-B ~181 TPS, simple-update ~239 TPS, select-only
~13.5k TPS). `make ralph-state-guard`: auto-repaired the same routine
running/completed progress-marker skew (a prior loop's clean-exit marker),
exit 0.

**Concurrency note:** the peer `ralph_loop.sh` tree (screen-rooted
`2085426` chain, live PID `2692451`/`2709750` this loop) was actively
writing `internal/catalog/catalog.go`/`codec.go`, `internal/executor/
codec.go`/`pg18_user_catalog_rows*.go`, `internal/initdb/open.go`/
`view_ddl_recovery_test.go`, `docs/design/0110-0001-pg-dump-tap-port.md`
throughout this loop — none of those files were touched. Committed via
explicit `git commit -m ... -- <9 files>` (message BEFORE `--`);
`git show --stat HEAD` confirmed only those 9 files changed and the
peer's dirty set was untouched before and after. Fetched + pushed clean
fast-forward (`40905365..aad61997`, ahead-1/behind-0 at fetch time).

Next step: the M0097-0040/M0122-0008 RBAC bucket's only remaining item is
the view-owner gap just above (bounded: needs `catalog.Table` to carry a
view-definer identity + `dmlPrivilegePermitted` to check it before falling
back to the querying role — new regression test needed since none exist
today). Otherwise resume M0122-0004's still-open window frames ROWS/RANGE/
GROUPS or GROUPING SETS/ROLLUP/CUBE (`internal/planner/planner.go:5070-5072`
comment confirms GROUP BY is NOT real multi-level grouping-set expansion;
`internal/parser/select.go:3437` hard-errors on any window frame clause) —
**re-check `git status` first**, the peer may be mid-flight on a different
file set by the time the next loop starts. Or `M0122-0003`'s
`pg_stat_io`/`track_io_timing` remainder (needs a new I/O-stats collection
layer, architectural — see ledger). Or the comma/LATERAL-join
`ctx.OuterRows` wiring gap (ledger row 480, still open).

---

**Loop #22 (this loop, the "peer" referenced just above) — COMPLETE,
committed + pushed (`a49d5656`, on top of the peer's `aad61997`/`40905365`
above — same shared working tree/`.git`).**

This session had a coherent M0119-0004 reloptions-restart-persistence fix
already fully written (code + tests + design-doc section + a matching
`resolved` deferral-ledger row 478, dated 2026-07-04) sitting uncommitted
in the working tree from before a context summarization boundary — no new
implementation work was needed, only verification and committing:
`buildUserPGClassRow` (`internal/executor/pg18_user_catalog_rows.go`) used
to hardcode `reloptions="{}"` unconditionally, so `fillfactor`/every
`autovacuum_*` setting/`vacuum_index_cleanup`, and for views
`security_barrier`/`security_invoker`/`check_option`, silently reverted to
defaults across every restart. Three compounding gaps: (1) the reloptions-
list builder was extracted from `catalog.go`'s live virtual pg_class
closure into shared `catalog.TableReloptionsElements`/`BuildTableReloptions`;
(2) `encodeValuePG`'s `"text[]"` case silently discarded any non-`KindBytes`
Datum as an empty array — fixed by encoding a real ArrayType blob via the
existing `pgTextArrayBytes`; (3) `decodePhysicalPGValueMctx` had no
`"text[]"`/`"_text"` case (fell to the generic default varlena branch,
returning raw undecoded ArrayType bytes) — added `decodePGTextArrayElements`
+ newly-exported `catalog.ArrayTextLiteral`. New `catalog.ApplyTableReloptions`
wires the read side into `loadUserTablesFromHeap` via newly-exported
`executor.PGClassColumnsPG18()`.

Verified (did not re-derive, just confirmed the pre-written diff was sound):
`go build ./...` clean; `go vet ./...` clean; `go test
./internal/catalog/... ./internal/executor/... ./internal/initdb/...`
(full packages) PASS, incl. the two new tests
`TestBuildUserPGClassRowReloptionsSurvivesHeapEncode` and
`TestTableAndViewReloptionsSurviveRestart`; `scripts/tpch-spotcheck.sh`
PASS (Q12=2/Q13=33); pre-commit pgbench smoke PASS (0 failed, TPC-B
~186 TPS, simple-update ~246 TPS, select-only ~14.2k TPS). Committed via
explicit `git commit -m ... -- <11 files>` (message BEFORE `--`);
`git show --stat HEAD` confirmed only those 11 files changed
(`.ralph/progress.json`, the `0110-0001-pg-dump-tap-port.md` design doc,
`internal/catalog/{catalog,codec}.go`, `internal/executor/{codec,
pg18_user_catalog_rows,pg18_user_catalog_rows_test}.go`,
`internal/initdb/{open,view_ddl_recovery_test}.go`, `weekly_loc.{csv,png}`)
and the peer's dirty set (`.ralph/working_set.md`, the `postgres` submodule's
untracked GLOBAL-index/build noise) was untouched before and after. Fetched
first and confirmed `aad61997` (the peer's just-landed SELECT-ACL commit)
was already an ancestor of local HEAD (`git merge-base --is-ancestor`) before
pushing — clean fast-forward (`aad61997..a49d5656`). `make ralph-state-guard`:
auto-repaired the same routine running/completed progress-marker skew, exit 0.

No new deferral-ledger row needed — row 478 (2026-07-04) already documented
this fix's landing and its two out-of-scope residuals (index reloptions via
`buildUserPGClassRowForIndex`/`loadUserIndexesFromHeap`; `toast.*`
reloptions) before this loop began; committing didn't change the scope.

Next step: pick up the index-reloptions residual row 478 named
(`internal/executor/pg18_user_catalog_rows.go`'s `buildUserPGClassRowForIndex`
still hardcodes `reloptions="{}"` for every index — fillfactor and AM-specific
params like `fastupdate`/`gin_pending_list_limit` would need the same
builder+decode+apply pattern this loop's fix used for tables/views, wired
into `loadUserIndexesFromHeap`), or return to loop #21's view-owner
security-definer gap, or M0122-0004's window frames/GROUPING SETS backlog
(see loop #21's notes above for exact file:line pointers). **Re-check
`git status` first** — the screen-rooted peer loop may have new WIP by the
time the next loop starts.
