# M0119-0006 (bt): the `rootdescend` tier — scoping recon

Status: LANDED 2026-09-22 — the tier is implemented. This document keeps the
recon that sized it, plus a MEASUREMENT CORRECTION and the live-verification
section at the end.
Kind: recon -> impl
Parent: M0119-0006
Movement: none

## Why this was looked at

`bt_index_check`/`bt_index_parent_check` accept four arguments. Three tiers are
implemented; `rootdescend` was recorded in the task as *"still a
call-shape-accepted no-op — upstream gates it to heapkeyspace v4"*.

That framing turns out to be wrong in the way that matters, so the recon is
worth recording even though no code changed.

## Finding 1 — upstream does not no-op; it ERRORS

`contrib/amcheck/verify_nbtree.c:479-485`:

```c
Assert(!state->rootdescend || state->readonly);
if (state->rootdescend && !state->heapkeyspace)
    ereport(ERROR,
            (errcode(ERRCODE_FEATURE_NOT_SUPPORTED),
             errmsg("cannot verify that tuples from index \"%s\" can each be found by an independent index search", ...),
             errhint("Only B-Tree version 4 indexes support rootdescend verification.")));
```

So upstream has exactly two behaviours: **run the tier** (v4), or **raise
0A000** (non-v4). Silently accepting the argument and checking nothing is
neither. A user running `pg_amcheck --rootdescend` against goopg is told the
index is clean by a verification that never executed — the worst of the three
outcomes, because it is indistinguishable from a real pass.

## Finding 2 — goopg is on the "run it" side of that gate

- `internal/initdb` pins the metapage at `btm_version = 4`
  (`btree_metapage_test.go`, `btree_index_bootstrap_test.go`).
- goopg's tuple key format puts the heap TID **inside** the key — the
  heapkeyspace tiebreaker, documented across `pgindex_btree.go`,
  `pgindex_tuplekey.go` and relied on by the `checkunique` tier, which has to
  compensate for it with a TID-blind comparator.

So the correct target is to RUN the tier, not to add the error. The error arm
is still needed, but only for the other key format (below).

## Finding 3 — the primitives already exist

| need | existing primitive |
|---|---|
| probe set (every leaf entry) | `amcheck.CollectBtreeLeafEntries(src, keyFmt)` → `[]nbtree.LeafEntry{Key, TID}` |
| independent search from the root | `(*nbtree.BTree).Search(key)` — `descendToLeaf` plus right-link recovery, which is `_bt_search`'s shape |
| opening the index | `openIndexBTree(ctx, idx, idxRel)` |
| tier wiring | the `btIndexCheckUnique` / `btIndexHeapAllIndexed` shape in `operators_bt_index_check.go` |

Using the *real* search path is faithful, not a shortcut: upstream deliberately
calls `_bt_search` rather than a verification-private walker, because the
property under test is "the normal search can find this tuple".

## The design the implementation should follow

Upstream asserts `key->heapkeyspace && key->scantid != NULL` — the tier
REQUIRES a key that carries the heap TID, because that is what makes the search
match one specific entry rather than the first of a duplicate group. goopg's
two key formats map straight onto upstream's gate:

- `keyFmt.KeyDesc() != nil` (TID inside the key) → **run** the tier:
  `Search(entry.Key)` is exact, so `!found` or a TID mismatch is a finding.
- otherwise → **raise 0A000** with upstream's message and hint verbatim.

Without that split the tier would be actively wrong on the blob format: with no
TID in the key, `Search` returns the first entry of a duplicate group and a TID
comparison would manufacture findings on a healthy index.

## Why it was not built in this loop, and the bar it must meet

Every tier already landed under M0119-0006 carries a **detection** test with a
non-vacuity guard — `TestBtIndexCheck_HeapAllIndexed*` plants a phantom tuple
and asserts the XX002 report; the `003*` ports inject real on-disk corruption.
A rootdescend tier that only proves "a healthy index still passes" would sit
below the bar this task has set for itself, and for a corruption-detection tool
that bar is the whole point: the failure mode being fixed here is precisely a
check that reports clean without looking.

Building it therefore needs, in one loop: the tier, the format split, a planted
inconsistency that the search genuinely fails to find, and the full executor
gate set. That is a loop's work on its own.

### One measurement the implementation must take first

Which key format the ported pg_amcheck tests' indexes actually use.
`buildPGIndexKeyDesc` accepts any btree index with key columns, which suggests
ordinary user indexes carry a descriptor and would take the run-it arm — but
this was inferred from the constructor's guards, not observed. It decides
whether the existing live expectation (`pg_amcheck --heapallindexed
--rootdescend` exit 0, recorded in the M0119-0006 br slice) still holds or
becomes an error, so it must be measured before the split is wired.

---

# Implementation (2026-09-22)

## A measurement correction to the recon above

The recon concluded goopg is on the "run it" side of upstream's gate, citing
`btm_version = 4` in initdb. That is true of the **bootstrap catalog index
metapages**, but it is not the property the tier needs, and the recon did not
check the right thing.

What the tier needs is the heap TID *inside the key*, and that is a per-index
property of goopg's key format. The measurement the filed task demanded found:

- `var pgIndexTupleKeys = true` (`pgindex_btree.go`) — the format flip is ON.
- So an ordinary index (default operator class and collation, a key type whose
  stored image is PG-faithful) gets a descriptor → **tuple** format → TID in the
  key → the tier can run.
- The shapes `buildPGIndexKeyDesc` refuses — expression keys, explicit operator
  classes, non-bytewise collations, types without a comparator — keep the
  **blob** format, which carries no TID in the key at all.

So **both arms are reachable in production**, which is what makes the gate a
real branch rather than a formality. A comment at the `keyFmt` site still said
"nil-descriptor / blob for every index today"; it was stale and is corrected.

## Shape

`amcheck.VerifyBtreeRootDescend` mirrors upstream's `_bt_search` +
`_bt_binsrch_insert` + `_bt_compare(...) == 0`: descend from the root to the
leaf the entry's key routes to, then confirm the entry is on it.

It descends over PageSource **copies** rather than calling
`(*nbtree.BTree).Search`. That was the recon's suggestion and it would have been
wrong: `pgindex_btree.go` documents that a stored *entry* key carries the row's
real TID while a *probe* key carries the zero TID (minus infinity), and that
"handing a probe an entry key would start a duplicate scan after some of its own
matches — a silent under-read, not a failure". Using the search API with an
entry key is precisely that misuse.

`RootDescendSupported(keyFmt)` is the gate; the executor raises upstream's
0A000 with its message and hint when it is false.

## Live verification, and the defect it caught

Both arms were exercised against the real `pg_amcheck` binary and at the SQL
surface on a scratch cluster:

| index | call | result |
|---|---|---|
| `rd_a` on `int` (tuple format) | `bt_index_parent_check(..., rootdescend := true)` | clean |
| `rd_expr` on `(a+1)` (blob format) | same | `ERROR: 0A000: cannot verify that tuples from index "rd_expr" can each be found by an independent index search` |
| `rd_a` | `pg_amcheck --heapallindexed --rootdescend` | exit 0 |

The last row is the br slice's recorded expectation, and it still holds.

**The live check earned its cost.** The refusal first surfaced as
`XX000: 0A000: cannot verify ...` — the correct SQLSTATE stringified *into the
message* — because `btIndexCheck`'s error path re-wrapped every tier error as an
internal error. No unit test could see it: the wrap happens only at that
boundary. `btIndexCheck` now returns an `*ExecError` unchanged and only wraps
genuine read errors.

## Test design

The detection arm is the point, and it is what a "healthy index still passes"
test cannot supply — the defect being fixed was a check that reported clean
without looking, so a test that cannot tell those apart would reproduce the
defect inside the suite.

Two leaf pages are swapped *through the PageSource* rather than by editing item
bytes. Every page stays individually valid, so the per-page tier still finds
nothing, and only the mapping between the search path and the page holding the
entry is broken — which isolates the property under test. Verified non-vacuous:
stubbing the descent to "found" fails exactly that arm with "the tier is not
actually searching from the root".

The healthy arm doubles as the routing check: if the descent's child-selection
rule disagreed with the one the tree was built under, a healthy tree would
report findings.
