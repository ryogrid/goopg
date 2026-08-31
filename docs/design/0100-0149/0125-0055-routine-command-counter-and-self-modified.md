# 0125-0055 — the routine command counter, and the `TM_SelfModified` error it unmasks

*Filed 2026-08-06. Closes M-NIGHTLY `AI-20260806-191958-001`
(`TestPort_IsolationEvalPlanQual`), which was the sole engine-side blocker of
the M0127 S7 acceptance gate.*

Successor to [0125-0052](0125-0052-dml-cte-write-fence-covers-whole-statement.md),
[0125-0053](0125-0053-dml-cte-preimage-reveal.md) and
[0125-0054](0125-0054-dml-cte-execution-order.md), which built goopg's stand-in for
PostgreSQL's per-tuple command id. This doc adds the piece those three left
implicit: **the command id is not constant for the duration of a statement.**

## 1. The gap, in one query

```sql
CREATE FUNCTION update_checking(int) RETURNS bool LANGUAGE sql AS $$
    UPDATE accounts SET balance = balance + 1 WHERE accountid = 'checking';
    SELECT true;$$;   -- VOLATILE (the default)

WITH doup AS (UPDATE accounts SET balance = balance + 1100
              WHERE accountid = 'checking'
              RETURNING *, update_checking(999))
UPDATE accounts a SET balance = doup.balance + 100 FROM doup RETURNING *;
```

| | rows returned | `checking` after |
|---|---|---|
| PG 18.3 | 1 (`savings`) | **1701** |
| goopg before | 1 (`savings`) | **1700** |

The RETURNING output agreed, so every previous loop's evidence agreed. The
divergence is in the heap: goopg's `update_checking` **did nothing at all.** Its
`UPDATE` scanned `accounts`, found no `checking` row it was allowed to see, and
reported 0 rows — silently, because a 0-row `UPDATE` is not an error.

This reproduces with no concurrency, one session, and no isolation harness. It
was found by shrinking the isolation permutation until the second session
disappeared.

## 2. Why the row was invisible

goopg's heap carries no per-tuple `cmin`/`cmax`. `Context.CTEWriteFence`
substitutes for `cmin`: every sub-statement of a data-modifying `WITH` registers
what it wrote, and every later scan of the same statement skips those tuples.
That reproduces the rule the WITH documentation states — "the sub-statements
cannot see one another's effects on the target tables" — and it is what
0125-0052/-0053/-0054 built.

But the fence was **command-blind**: once registered, a tuple was hidden from
every scan for the rest of the statement. PostgreSQL's rule is narrower. In
`HeapTupleSatisfiesMVCC`
(`postgres/src/backend/access/heap/heapam_visibility.c`) the tuple is hidden
only while

```
cmin >= curcid
```

and `curcid` is **not** frozen at the statement's `es_output_cid`. For a
routine that is not `readonly_func`, PostgreSQL advances it:

```c
/* postgres/src/backend/executor/functions.c, postquel_getnext */
if (!fcache->func->readonly_func)
{
    CommandCounterIncrement();
    if (!pushed_snapshot) { PushActiveSnapshot(GetTransactionSnapshot()); ... }
    else                    UpdateActiveSnapshotCommandId();
}
```

with upstream's own comment: *"If not read-only, be sure to advance the command
counter for each command, so that all work to date in this transaction is
visible."* `readonly_func` is set in `init_sql_fcache` from
`provolatile != PROVOLATILE_VOLATILE`, so **VOLATILE routines advance, STABLE
and IMMUTABLE ones do not.** PL/pgSQL reaches the same place through SPI, which
increments per statement unless the plan is read-only.

So `update_checking` runs one command id past the statement that called it, and
the row the CTE just wrote (`cmin` = that statement's `es_output_cid`) is
visible to it. goopg hid it.

## 3. The fix: give the fence a command id

`Context.CmdID` is the context's command id **relative to the enclosing
statement's `es_output_cid`** — 0 while the statement's own plan runs (its CTEs
and its body alike), one higher per nested VOLATILE routine body.
`routineCommandCounterIncrement` (`operators_cte_dml.go`) is the
`CommandCounterIncrement` analogue; it is called at each of the six sites in
`plpgsql_runtime.go` that build a routine body's child `Context`, and returns
without incrementing for `provolatile in ('s','i')`.

The two fence maps stop being sets and become `map[CTEFencePtr]int`, valued by
the writing/killing command id — the missing `cmin` and `cmax`. The three
consult helpers then *are* the upstream comparisons:

| helper | test | upstream arm |
|---|---|---|
| `cteFenced` | `writeCmd >= ctx.CmdID` | `cmin >= curcid ⇒ invisible` |
| `cteRevealed` / `cteRevealFor` | `killCmd >= ctx.CmdID` | `cmax >= curcid ⇒ the delete has not happened yet ⇒ show the pre-image` |
| `cteWrittenByLaterCommand` | `writeCmd > ctx.CmdID` | `tmfd.cmax != es_output_cid` (§4) |

Every statement with no data-modifying `WITH` in flight has both maps nil, so
the fast path is one nil check, exactly as before.

## 4. What the fix unmasks: `TM_SelfModified`

Restoring the function's write makes the second half of the upstream behaviour
reachable. Under the isolation permutation `wx1 updwctefail c1 c2 read` a second
session updates `checking` first, so the outer `UPDATE` finds its target
concurrently updated and runs EvalPlanQual. The chain-follow applies **no** cmin
test (`heapam_tuple_lock` with `TUPLE_LOCK_FLAG_FIND_LAST_VERSION` just walks
`t_ctid`), so it leads straight into a version this statement's own sub-command
produced. PostgreSQL refuses to merge that with the original update:

```
ERROR:  tuple to be updated was already modified by an operation triggered by the current command
HINT:   Consider using an AFTER trigger instead of a BEFORE trigger to propagate changes to other rows.
```

from `nodeModifyTable.c` `ExecUpdate:2656` (confirmed by
`log_error_verbosity=verbose` against live PG 18.3), SQLSTATE **27000**
(`ERRCODE_TRIGGERED_DATA_CHANGE_VIOLATION`). The discriminator is
`tmfd.cmax != estate->es_output_cid`: killed by a LATER command ⇒ error; killed
by the SAME command ⇒ ignore the redundant update silently, which is the
`updwcte` permutation and remains a `continue` in goopg.

goopg raises it at the two EPQ chain-follow sites the permutations reach:
`updateOp.updateWithFrom` and `deleteOp.deleteWithUsing`. The sentinel pair
`errTupleAlreadyModifiedBy{Update,Delete}` became the constructor
`errTupleAlreadyModified(verb, pos)` — it carries a per-call `Pos`, and stamping
that onto a shared `*ExecError` would be a data race between backends. Its code
was `09000` (`ERRCODE_TRIGGERED_ACTION_EXCEPTION`, a different class); this is
the first time the path was reached end-to-end, so nothing had contradicted it.

### Retired with it

`CTENewToOld`, `CTESelfModifiedErrors` and `CTESelfModErr` were an earlier,
pointer-chasing approximation of the same rule: they recorded the *pre-CTE*
tuple and had `scanMatching` raise on encountering it. Two problems, both now
moot. They were **unreachable** — populating them required a CTE to update a row
another sub-statement of the same statement had written, which the fence itself
prevents — and had the command-counter fix landed without touching them, they
would have started firing on the **non**-concurrent query of §1, where PG
returns success. The command-id model subsumes them and they are deleted.

## 5. Verification

Bar: byte-identical to live PG 18.3 on the four reachable forms of the query —
`updwctefail`/`delwctefail`, each with and without the concurrent session —
comparing the returned rows AND the final heap. All four match, including the
error message and the resulting `checking = 400 / savings = 600`.

- `TestPort_IsolationEvalPlanQual` **PASSES** (22 s; was the S7 blocker).
- New `internal/executor/cte_dml_command_counter_test.go`: the VOLATILE case
  (`checking` reaches 1701) and its negative twin, a **STABLE** routine that
  must still be blind to the caller's writes — without which "always advance"
  would pass too.
- Full `TestPort_Isolation*` (407 s), full `TestPort_RegressSuite` (322 s),
  `internal/executor` + `internal/server` + `internal/planner` + `internal/mvcc`,
  units gate, `tpch-spotcheck` Q12=2 / Q13=35.

## 6. Deferred

`deleteWithUsing` still has no EvalPlanQual: on a concurrent update it skips the
victim instead of re-fetching the live version and re-evaluating the `USING`
predicate. This change adds only the `TM_SelfModified` detection at that site —
the chain is followed far enough to see a later-command version and error, and
no further — guarded on `len(ctx.CTEWriteFence) > 0` so every statement without
a data-modifying `WITH` stays on the pre-existing path. Ledger row 2026-08-06;
resume point `operators_storage.go` `deleteOp.deleteWithUsing`, mirroring the
`epqWait` → `epqFollowHOT` → `epqFollowChain` → re-evaluate loop that
`deleteOp.Next` already has.
