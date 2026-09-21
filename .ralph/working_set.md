(idle — nothing in flight)

# Loop #68 result — M0119-0006 (bt): rootdescend tier SCOPED, premise corrected

Banner: nightly run id UNCHANGED (20260922-004850), so item 10's
PgoutputInterop stays non-selectable (its next step is "wait for the next
nightly"). Item 10 has nothing else open, so per the banner's pre-existing
milestone order (M0119 first, which is NOT document order) the selectable
task was **M0119-0006**, whose only unbuilt piece is the `rootdescend` tier.
No production change this loop.

## The finding: the task's own premise was wrong
M0119-0006 recorded rootdescend as "a call-shape-accepted no-op (upstream
gates it to heapkeyspace v4)". Upstream does NOT no-op on non-v4 —
`verify_nbtree.c:479-485` raises `ERRCODE_FEATURE_NOT_SUPPORTED` with
"cannot verify that tuples from index %q can each be found by an
independent index search" + hint "Only B-Tree version 4 indexes support
rootdescend verification." Two behaviours only: RUN or ERROR. Accepting the
argument and checking nothing is neither — and is the WORST of the three,
because a clean report from a check that never executed is
indistinguishable from a real pass.

## goopg is on the RUN side, so the target is to implement it
- `internal/initdb` pins `btm_version = 4` (two tests).
- The tuple key format carries the heap TID INSIDE the key — the
  heapkeyspace tiebreaker that `checkunique` already compensates for.

## Primitives already exist (sized, not guessed)
`amcheck.CollectBtreeLeafEntries` (probe set, `LeafEntry{Key,TID}`),
`(*nbtree.BTree).Search` (`descendToLeaf` + right-link recovery = `_bt_search`
shape; using the REAL search path is faithful, since the property under test
is "the normal search finds this tuple"), `openIndexBTree`, and the existing
`btIndexCheckUnique`/`btIndexHeapAllIndexed` tier shape.

## Why not built this loop
Every tier landed under M0119-0006 carries a DETECTION test with a
non-vacuity guard (HeapAllIndexed plants a phantom tuple → XX002; the 003
ports inject real on-disk corruption). For THIS tier the gap matters most:
the defect being fixed is a check that reports clean without looking, so a
test that only shows "a healthy index still passes" would reproduce the
defect inside the test suite. Tier + format split + a planted unreachable
entry + the executor gate set is a loop on its own.

## Next step — filed as M0119-0006bt-rootdescend
MANDATORY format split: `keyFmt.KeyDesc() != nil` → run
(`Search(entry.Key)` is exact); else raise 0A000 verbatim. Without it the
tier is ACTIVELY WRONG on the blob format — no TID in the key means `Search`
returns the first of a duplicate group and a TID comparison manufactures
findings on a healthy index.
MEASURE FIRST: which key format the ported pg_amcheck tests' indexes use.
`buildPGIndexKeyDesc` accepts any btree index with key columns, suggesting
the run-it arm — but that is INFERRED from the constructor's guards, not
observed, and it decides whether the br slice's live `--rootdescend` exit-0
expectation still holds.

## Gates
`go build ./...` OK; state guard OK; pgbench smoke via hook. No value gates —
zero production diff (verified over `internal/ cmd/`).

## Owner escalations OPEN — three
1. M0145-0018 cost-model no-go. 2. M0145-0001 lineage exhausted (blocks all
of item 3). 3. partition_aggregate's inventory row marks a never-passing
case must-pass.
