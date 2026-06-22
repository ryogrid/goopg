Last loop (#31): M0118-0008 — landed the **inherit-temp foundation** (design
0118-0036). NOT a spec promotion (inherit-temp stays `defer`). Commit + push
pending.

What landed (zero blast radius — nothing reads TempOwner in live paths yet):
- `catalog.Table.TempOwner string` — owning session's token ("" = permanent or
  session-less temp = visible to all).
- `catalog.AccessibleInheritanceChildren(children, sessionTempOwner)` — shared
  filter that drops other-session temp children (keeps permanent/own/unowned).
  The single chokepoint the wiring loop will call at every expansion site.
- `config.SessionRegistry.UniqueID() uint64` — process-unique, non-zero,
  lifetime-stable per-connection id (atomic counter in NewSessionRegistry).
- `executor.sessionTempOwner(ctx)` — derives "s<id>" from
  ctx.AdvisorySessionIdentity (UniqueID interface); stamped at BOTH CREATE
  TEMPORARY TABLE sites (operators_ddl.go base ~1949 + partition leaf ~2936).
- Units: TestAccessibleInheritanceChildren / TestSessionRegistryUniqueID /
  TestSessionTempOwner. fix_plan + ledger + design README updated.

Gates run: catalog/config/executor packages green; full executor pkg PASS;
gofmt clean on touched files; build ./... OK; pgbench smoke via pre-commit hook.

WHY foundation-only: faithful RELATION_IS_OTHER_TEMP exclusion is MULTI-SITE
(planner SELECT collectInheritanceDescendants planner.go:2189 / :2142 +
executor UPDATE storage.go:3122, DELETE :3836/:4658, UPDATE…FROM :4252,
TRUNCATE/DDL operators_ddl.go, FK/MERGE/VACUUM). Sibling-paths discipline: must
wire ALL atomically or a missed site = silent row-count regression. Planner
SELECT site has no session identity — needs a CurrentTempOwner() channel on
sessionPlanCatalog (dispatch.go:870) + currentTempOwner(cat) walk in
planScanRangeVar threaded into the collect funcs.

Next step (resume = the deferred WIRING loop): call
`catalog.AccessibleInheritanceChildren` at every site above (executor:
owner=sessionTempOwner(ctx); planner: thread the token), then verify strict
9-perm `TestPort_IsolationInheritTemp` byte-for-byte — incl. the last two
permutations' TRUNCATE-on-parent blocking (s2_select_p waits behind s1's
in-progress TRUNCATE; s2_select_c on its own temp child does not). Full gates:
regress-port + pgbench + -race executor + full TestPort_Isolation*.

Other deferred M0118 tail (hard, see ledger): alter-table-{1,2,4}, *-conflict
(role/GRANT infra), partition ATTACH/DETACH-concurrently, vacuum-no-cleanup-lock,
reindex-toast, plpgsql-toast; fk-deadlock, ri-trigger, eval-plan-qual,
predicate-hash/gin/gist, horizons, intra-grant-inplace, 2PC/async-notify.
