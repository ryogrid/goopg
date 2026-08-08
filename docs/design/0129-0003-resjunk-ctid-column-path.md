# 0129-0003 — resjunk-ctid column path re-enable

| field | value |
| --- | --- |
| status | **accepted** — implemented 2026-08-08 (m0129 branch, commit pending) |
| date | 2026-08-08 |
| milestone | `docs/milestones/0129-q74-fix-and-m0128-followups.md` |
| task | M0129-S6 |
| supersedes | (none) |
| primary source | `internal/planner/planner.go:1624-1636` (disable site), ledger rows 2026-08-06 root-0038/M0128-P6.1 |

## 1. Problem

goopg carries tuple IDs through the executor two ways:

| path | mechanism | status |
| --- | --- | --- |
| **Column** | resjunk `ctid` column in the row, read via `LockedRel.CtidResno` | **disabled** (planner) |
| **Slot side-channel** | `MaterializedSlot.hasCTID`/`ctidBlock`/`ctidOff` | active (default) |

The column path is the PG-faithful approach. PG's `preprocess_targetlist`
(`postgres/src/backend/optimizer/prep/preptlist.c:214-287`) adds `resjunk` ctid
Var entries to the target list BEFORE the join tree is built, so they propagate
through every intermediate node automatically. goopg's `wireRowMarkCtidColumns`
(`internal/planner/planner.go:1993`) attempted the same but adds ctid columns to
scan schemas AFTER parent nodes are built. goopg's plan tree stores schemas
eagerly at construction time, so intermediate-node schemas are snapshotted and
stale the moment a leaf scan gains a column. The column path was disabled
(`planner.go:1636` `numCtid := 0`) because adding ctid to a SeqScan schema after
parent nodes are built caused column misalignment — in self-joins the ctid
leaked into the right child's output positions (`eval-plan-qual` `partiallock`
returned 0 rows).

The executor-side column path is **fully implemented and correct**:
- `seqScanOp.Next` (`operators_storage.go:1773-1777`): when the scan schema has
  trailing ctid columns (`len(schema) > len(cols)`), appends `"(block,off)"`
  string datums to the row.
- `lockRowsOp.drainAndStamp` (`operators_lockrows.go:1012-1022`): reads ctid
  from `row[lk.CtidResno]` via `parseRowCTID`.
- `LockRows.Output()` (`plan.go:1852-1861`): strips trailing `NumCtidCols` from
  the child output so the user never sees junk columns.

Only the planner wiring is broken.

## 2. Design

### 2.1 Chosen approach: bottom-up schema recomputation

**Decision: approach (a) — recompute intermediate-node schemas after ctid injection.**

Approach (b) (inject ctid at scan creation, PG's `preprocess_targetlist` timing)
was rejected for this milestone. PG's targetlist-driven approach works because
Vars are `varno`-addressed and target lists flow through every node type
naturally. goopg's schema-based architecture stores `Schema []SchemaColumn` on
every node that produces output, and `wireRowMarkCtidColumns` already does 90%
of the work — the ONE missing piece is recomputing intermediate-node schemas
after leaf injection. Threading lock info to scan creation sites would touch
`planFromClause`/`planScanRangeVar`/`planFromItem` and every join-search path
that constructs scans, a far larger surface for a one-loop task.

### 2.2 How schema delegation works today

Most plan node types delegate `Output() Schema` to their child (from `plan.go`):

| node | `Output()` |
| --- | --- |
| `Filter`, `Sort`, `Limit`, `Memoize` | `Child.Output()` |
| `SetOp` | `Left.Output()` |
| `CTEDMLPrefix` | `Body.Output()` |
| `LockRows` | `Child.Output()` with trailing `NumCtidCols` stripped |
| `Distinct`, `DistinctOn`, `OrdinalityWrap` | own `n.schema` (but set from child) |

These nodes **auto-correct** when a leaf schema changes — they have no stored
schema, so `Output()` always reflects the current child.

### 2.3 Nodes that store their own schema

Only two intermediate node types store a schema that must be explicitly
recomputed when a child changes:

- **`Join`** (`plan.go:957`): returns `n.schema` for INNER/LEFT/RIGHT/FULL types
  (Semi/Anti return `n.Left.Output()`). `n.schema` is set at construction as
  `append(left.Output(), right.Output()...)`.
- **`NestedLoopIndexJoin`** (`plan.go:753`): returns `n.schema`, set at
  construction as `append(Outer.Output(), Inner.Output()...)`.

Leaf nodes (`SeqScan`, `IndexScan`, `Values`, CTE scans, etc.) have their
schemas set at construction and are the ones `wireRowMarkCtidColumns` modifies.
They need no further recomputation.

### 2.4 Algorithm

Three-step fix inside `planSelect`'s locking block (planner.go:1612-1654):

**Step 1 — Inject ctid at leaves (existing, re-enable):**
```go
numCtid := wireRowMarkCtidColumns(out, locks)
```
Unchanged from the existing (disabled) implementation. This:
- Walks the tree to find `SeqScan`/`IndexScan` for each locked relation
- Appends `SchemaColumn{Name: "ctidN", Type: tid}` to each such scan's schema
- Extends the top `Project` with `ColumnRef` targets and matching schema columns
- Sets `locks[i].CtidResno`

**Step 2 — Recompute intermediate schemas (NEW):**
```go
if numCtid > 0 {
    recomputeIntermediateSchemas(out)
}
```

The function does a post-order traversal: process children first, then recompute
the parent from the children's now-updated `Output()`. Only `Join` and
`NestedLoopIndexJoin` need handling; everything else delegates correctly or is a
leaf.

```go
func recomputeIntermediateSchemas(root Node) {
    var walk func(n Node)
    walk = func(n Node) {
        if n == nil {
            return
        }
        switch v := n.(type) {
        case *Join:
            walk(v.Left)
            walk(v.Right)
            if v.Type != JoinTypeSemi && v.Type != JoinTypeAnti {
                v.schema = appendSchema(v.Left.Output(), v.Right.Output())
            }
        case *NestedLoopIndexJoin:
            walk(v.Outer)
            walk(v.Inner)
            v.schema = appendSchema(v.Outer.Output(), v.Inner.Output())
        case *SetOp:
            walk(v.Left)
            walk(v.Right)
        // Pass-through nodes: recurse only; Output() delegates to child.
        case *Project:
            // Already extended by wireRowMarkCtidColumns; still recurse for
            // any scan below it (in the join tree under the Project).
            walk(v.Child)
        case *Filter, *Sort, *Limit, *Distinct, *DistinctOn,
             *OrdinalityWrap, *Memoize, *LockRows:
            walk(childOf(v))
        case *Aggregate, *WindowAgg:
            walk(childOf(v))
        case *Insert, *Update, *Delete:
            walk(childOf(v))
        // Leaves: SeqScan, IndexScan, IndexOnlyScan, Values, CTEScan, etc.
        // No children to walk; schemas already correct.
        }
    }
    walk(root)
}
```

**Why this is sound:** `appendSchema` (already defined at `planner.go:528`) is
the same function used during original construction. The recomputation is
deterministic: the scan schemas are the ONLY thing that changed, and the
post-order guarantees children are current when a parent is recomputed.

**Step 3 — Wire `NumCtidCols` on LockRows (existing):**
```go
out = &LockRows{..., NumCtidCols: numCtid}
```
Unchanged. `LockRows.Output()` strips these columns from user-visible output.

### 2.5 Edge cases

**Self-joins.** The `nextLockIdx` map in `wireRowMarkCtidColumns` already handles
multiple scans of the same relation — each scan gets its own ctid column and
the `nextLockIdx` advances through matching `LockedRel` entries. After schema
recomputation, each scan's ctid column appears at the correct position in the
join output.

**No locked scans found.** `wireRowMarkCtidColumns` returns 0 when no scan
matches a locked relation (CTE scan, VALUES, etc.). `recomputeIntermediateSchemas`
is skipped. The executor falls back to the slot side-channel as today.

**LockRows wrapping.** `wireRowMarkCtidColumns` is called when `root` is the
`Project` (before `LockRows` is wrapped). The recomputation also runs before
`LockRows` wrapping. The ctid columns flow through `LockRows.Child.Output()` and
are stripped by `LockRows.Output()` — the user never sees them.

**Distinct/DISTINCT ON above LockRows.** These nodes wrap AFTER the lock block.
They see `LockRows.Output()` (ctid-stripped). Correct — PG also strips junk
columns.

### 2.6 What the slot side-channel still covers after this fix

After re-enabling the column path, the slot side-channel (`MaterializedSlot.hasCTID`)
remains as belt-and-braces for plan shapes where `wireRowMarkCtidColumns` cannot
wire a column:

| shape | column path | slot side-channel |
| --- | --- | --- |
| Plain SeqScan FOR UPDATE | ✓ | ✓ (fallback) |
| Join FOR UPDATE | ✓ | ✓ (fallback) |
| CTE scan FOR UPDATE | ✗ (no scan to wire) | ✓ |
| VALUES FOR UPDATE | ✗ (no scan to wire) | ✓ |
| Sort spill > work_mem | ✓ (column survives spill) | ✗ (`ctidsDisabled`) |

The last row is the root-0038 ledger row — the column path fixes the sort-spill
TID-loss gap by construction (a ctid datum in the row survives the spill like any
other column), which is why S6 subsumes S3.

## 3. Verification

### 3.1 Gates

- **UNITS**: `RALPH_PRECOMMIT_SCOPE=units scripts/ralph-precommit-test.sh`
- **ISOLATION**: `go test -v -run 'TestPort_IsolationEvalPlanQual' ./internal/testport/`
  — the `partiallock`/`lockwithvalues` permutations must PASS (these are the
  exact cases the column-path disable was protecting)
- **SPOT**: `scripts/tpch-spotcheck.sh` (Q12=2/Q13=35)
- **DS05**: `scripts/tpcds-sf05-regression.sh sweep` (zero row/checksum deltas)

### 3.2 Unit test

A planner unit test (`TestPlanCtidRowMarkWiring` in `locking_test.go`) already
exists but is disabled. Re-enable it after the fix. Add a self-join `FOR UPDATE`
case — the exact shape that broke before.

### 3.3 Retire decision

After the column path is verified green, decide whether the slot side-channel:
- **Retires**: removed from `lockRowsOp`, `sortOp`, `joinOp`, `MaterializedSlot`
- **Stays**: kept as belt-and-braces for shapes the column path cannot cover
  (CTE scans, VALUES)

Recommendation: **keep** — the side-channel covers shapes (CTE scans, VALUES)
that `wireRowMarkCtidColumns` cannot wire, and the fallback costs one bool check
per locked row on the hot path.

## 4. Relationship to other M0129 tasks

- **S3 (sort-spill ctid):** the column path fixes the spill TID-loss gap — a ctid
  datum in the row survives the sort spill like any other column. After S6 lands,
  S3 reduces to verifying the spill shape carries the column and closing the
  root-0038 ledger row.
- **S2 (deleteWithUsing EPQ):** orthogonal — uses EPQ recheck, not row marking.
