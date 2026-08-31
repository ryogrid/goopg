# root-0031 — table inheritance did not survive a restart (pg_inherits persistence)

**Status:** implemented (2026-07-28)
**Task:** M-NIGHTLY — `regress/{errors, …}` genuine sub-timeout divergences
(AI-20260725-011243-008..-026 survivors, filed after root-0029 reclassified the
19 phantom items)
**Touches:** `internal/executor/sys_pg_inherits.go` (new),
`internal/executor/operators_ddl.go`, `internal/initdb/catalog_heap_reload.go`,
`internal/initdb/open.go`, `internal/catalog/catalog.go`,
`internal/testport/inheritance_pg_inherits_durability_test.go` (new)

## 1. What the nightly reported, and what it actually was

The nightly batch reported a set of `TestPort_RegressSuite` cases as
`output mismatch; normalization rules need extension`. The prior loop's triage
recorded the surviving hypothesis as **suite-ordering state leakage** — "a case
mutating shared `test_setup` fixtures" — because `errors`, `portals_p2` and
`select` pass in isolation and only diverge inside the full suite.

The ordering dependence is real, but the mechanism is not fixture mutation.
Bisecting the alphabetical prefix (the suite runs cases in `filepath.Glob` order,
not `parallel_schedule` order) never converged: the same range failed once and
passed on the next three runs. The variable is not *which* cases ran before, it
is **whether the cluster restarted**:

```
regress_suite_test.go:104: previous case timed out; restarting the cluster …
regress_suite_test.go:115: cluster recovered
```

The suite restarts its cluster after any case burns the 120 s budget
(root-0029's `clusterPoisoned` recovery). Under nightly co-load that happens
often; in an isolated `-run '…/errors$'` repro it never happens. **Everything
after a restart ran against a catalog that had silently lost every inheritance
relationship.**

## 2. Root cause

`pg_inherits` was a purely **virtual** catalog. Its rows were synthesized on
demand by `catalog.PGInheritsRowsForDBOid` from two in-memory fields —
`Table.InheritsParentOIDs` and the `InMemory.inheritanceChildren` map — and the
only writer of either was `CREATE TABLE … INHERITS` itself (plus ALTER TABLE …
INHERIT via `ApplyPendingInheritanceChanges`). Nothing wrote the parent→child
edge to disk and no reload pass rebuilt it, so a restart dropped all of it:

| observable | fresh server | after restart (before this fix) |
|---|---|---|
| `SELECT count(*) FROM pg_inherits` | 4 | **0** |
| `SELECT count(*) FROM person` (3 rows across the hierarchy) | 3 | **0** |
| `ALTER TABLE emp RENAME COLUMN salary TO manager` | `ERROR: column "manager" of relation "stud_emp" already exists` | **succeeds — `emp` ends up with two `manager` columns** |

The last row is the one that turned a durability gap into a *correctness* bug:
`renameatt`'s child name-collision recursion iterates `InheritanceChildren`, so
with no children registered it checks nothing and the rename lands.

Reproducer, five statements against a plain cluster — no regress harness needed
(`postgres/src/test/regress/sql/test_setup.sql` is where the
`person ← emp/student ← stud_emp` fixture comes from):

```sql
CREATE TABLE person (name text, age int4);
CREATE TABLE emp (salary int4, manager name) INHERITS (person);
CREATE TABLE student (gpa float8) INHERITS (person);
CREATE TABLE stud_emp (percent int4) INHERITS (emp, student);
-- restart the server here
ALTER TABLE emp RENAME COLUMN salary TO manager;   -- must raise 42701
```

## 3. The fix

### 3.1 pg_inherits becomes heap-backed (the primary fix)

Modelled exactly on B5 Slice B's treatment of `pg_attrdef`, which faced the same
problem (the reloaded catalog cannot rebuild `DefaultExpr` from `pg_attribute`):

* **Writer** — `internal/executor/sys_pg_inherits.go`'s `writeInheritsRow`
  appends a canonical PG18 `pg_inherits` tuple
  (`inhrelid, inhparent, inhseqno, inhdetachpending`) to `base/<dbOid>/2611`,
  routed per-DB through `tableCatalogHeapDBOid` like the `pg_class` /
  `pg_attribute` / `pg_attrdef` rows written beside it. `syncTableToCatalogHeap`
  — the single funnel every table-persisting DDL path passes through — emits one
  row per entry of `tbl.InheritsParentOIDs`, with the 1-based `inhseqno`
  preserving the `INHERITS (…)` declaration order that `pg_dump` re-emits.
  Heap-only, no index (2680 is not materialized in goopg), same as `pg_attrdef`.
* **Stale rows** — the `deleteCatalogRowsForOID` path stamps `xmax` on the
  relation's `pg_inherits` rows (matching `inhrelid`, bytes 0:4) alongside its
  existing `pg_attribute` / `pg_attrdef` / `pg_rewrite` stamping, so a DROP, an
  ALTER re-sync, or a rolled-back CREATE leaves no resurrectable edge.
* **Reload** — `loadInheritanceFromHeap` (`catalog_heap_reload.go`) seq-scans
  each database's 2611 heap, groups by child, sorts by `inhseqno`, and restores
  **both** halves of the in-memory state: the child's ordered
  `InheritsParentOIDs` and the catalog's parent→children registry. It runs as a
  standalone unconditional pass in `open.go`, immediately after the `pg_attrdef`
  pass — it must follow *every* table-load pass because parent and child may
  reload in either order (and because the M0114 catalog cache bypasses
  `loadUserTablesFromHeap`, so it cannot live inside it). An edge whose child or
  parent no longer exists is skipped rather than failing startup, matching the
  sibling passes.

Partition children are deliberately **not** written: their parent link rides
`PartitionParentOID`, and a partitioned parent (`relkind='p'`) is not reloaded at
all today (`loadUserTablesFromHeap` takes `'r'` and `'m'`), so a persisted edge
would dangle. See the deferral-ledger row.

### 3.2 Two ALTER TABLE rename divergences the restart had been masking

Both are PG-fidelity bugs in their own right; the restart merely exposed them.

* `catalog.RenameTable` reported the schema-**qualified** catalog key
  (`relation "public.stud_emp" already exists`). PG's `RenameRelationInternal`
  uses the bare name (`tablecmds.c`: `errmsg("relation \"%s\" already exists",
  newrelname)`). The qualified form only appeared once a table carried a
  non-empty `Schema` — i.e. for every table reloaded from the `pg_class` heap —
  so the *message text itself* differed between a fresh and a restarted server.
* `ALTER TABLE … RENAME COLUMN` checked the inheritance children for a name
  collision but never the target relation's **own** columns. PG's
  `renameatt_internal` recurses into the children first (which is why a conflict
  names the child) and then calls `check_for_column_name_collision` on the
  relation itself. goopg had only the first half, so on a childless table —
  or on any table at all once a restart had dropped the edges —
  `ALTER TABLE t RENAME COLUMN a TO b` with an existing `b` silently produced a
  table with two columns named `b`. The new check is placed *after* the child
  recursion so the PG message ordering is preserved.

### 3.3 DROP AGGREGATE signature resolution

With the above fixed, one `regress/errors` line remained, and it was a genuine
instance of the ordering dependence the task described — visible only when the
`aggregates` case's `create_aggregate.sql` pre-setup has already defined
`newcnt`:

* `DROP AGGREGATE newcnt (nonesuch)` **silently succeeded**. The registry
  lookup ran before the argument-type validation, so a bare name match dropped
  the aggregate and the unresolvable type was never reached. PG's `RemoveObjects`
  resolves the signature first (`LookupAggNameTypeNames` → `typenameTypeId`) and
  raises `type "nonesuch" does not exist` regardless of the name. The check is
  now hoisted above the registry lookup, restricted to unqualified type names so
  the existing `3F000 schema does not exist` branch keeps winning for
  `someschema.sometype` (PG resolves the namespace first too).
* Fixing that exposed a second bug the first had been cancelling out:
  `DROP AGGREGATE newcnt (float4)` dropped a `newcnt("any")`, because the
  registry is keyed by name alone and the DROP ignored `ArgTypes` entirely (a
  known gap, called out in the pre-existing code comment).
  `dropAggregateSignatureMatches` now compares the statement's argument list
  against the registered `UserAggregate.ArgTypes` — through
  `dropCompatCanonicalType` so alias forms agree (`float4` ≡ `real`), with a raw
  case-insensitive fallback for names it does not canonicalise (`"any"`, user
  types). A mismatch falls through to the existing "does not exist" path. A DROP
  with no argument list at all stays lenient (`DROP AGGREGATE name` is goopg's
  established shorthand). This is a targeted fix at the DROP site, **not** a
  registry redesign — the registry is still name-keyed, so two live overloads of
  one name still cannot coexist (ledger row).

## 4. Verification

* `TestPort_InheritanceSurvivesRestart` (new) — builds the upstream
  `person ← emp/student ← stud_emp` fixture, asserts edge count, `inhseqno`
  order, parent-scan expansion (`count(*)` vs `count(*) FROM ONLY`), that a
  dropped child leaves no edge, and that the post-restart rename still raises
  PG's 42701 naming the child. **Negative control run**: with the
  `loadInheritanceFromHeap` call disabled the test fails at
  `post-restart pg_inherits row count = "0", want 4`, so it is not vacuous.
* `regress/errors` — the target case. Reproduced the nightly's failure with an
  alphabetical **prefix** of the suite (63 cases, ~3.5 min) instead of the full
  ~1 h run: 8 divergent lines before, `PASS` after, in a run that took the
  cluster-restart path (`restarts=1`). Prefix pass count 9 → 10.
* `go test ./internal/executor/ ./internal/catalog/ ./internal/initdb/` — PASS.
* `RALPH_PRECOMMIT_SCOPE=units scripts/ralph-precommit-test.sh` — PASS.
* `scripts/tpch-spotcheck.sh` — PASS (Q12 rows=2, Q13 rows=35).
* TPC-DS SF0.5 gate **not** run: no TPC-DS query uses table inheritance,
  `DROP AGGREGATE`, or `ALTER TABLE … RENAME`, and the change adds catalog-heap
  rows only on those DDL paths. The write path is otherwise inert for a
  query-only sweep.

## 5. Why this was mis-triaged for several loops

Worth recording, because the same shape will recur. The failure looked
ordering-dependent and *was* ordering-dependent, which made "a case mutates the
shared fixture" the obvious reading — and bisecting for the mutating case is the
obvious next step. It cannot converge: the trigger is a **timeout** in some
earlier case, which is load-dependent, so the bisect's signal flips between runs
of the same range. The tell was in the log all along
(`previous case timed out; restarting the cluster`), one line above the first
diverging case. When a suite-ordering bisect gives inconsistent answers for an
identical range, suspect a *nondeterministic* harness action (restart, retry,
recovery) before suspecting a subtler ordering dependency.

## 6. Follow-ups

Filed in `.ralph/deferral_ledger.md` (2026-07-28, root-0031):

1. Partitioned tables (`relkind='p'`) are not reloaded at all — `CREATE TABLE …
   PARTITION BY` + restart yields `relation "pt" does not exist`. Verified
   independently of this change; blocks persisting partition `pg_inherits` rows.
2. `ALTER TABLE … INHERIT / NO INHERIT` does not route through
   `syncTableToCatalogHeap`, so an edge added or removed after CREATE is still
   restart-volatile.
3. `initdb/relcache_init.go`'s nailed `pg_inherits` tupledesc declares 3
   attributes; PG18's has 4 (`inhdetachpending`, added in PG14). goopg now
   writes 4-column tuples, so a PG standby reading `base/<db>/2611` through the
   nailed descriptor would misparse them.
4. The aggregate registry remains name-keyed: two live overloads of one
   aggregate name still cannot coexist.
5. The regress harness runs cases in `filepath.Glob` (alphabetical) order rather
   than `parallel_schedule` order. Not the cause here, but it is a real
   pg_regress infidelity — `alter_table` runs before `errors` in goopg and after
   it upstream — and it is a standing source of cross-case coupling.
