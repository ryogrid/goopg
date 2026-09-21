(idle — nothing in flight)

# Loop #69 result — M0119-0006bt-rootdescend LANDED

Banner: nightly run id UNCHANGED (20260922-004850), so PgoutputInterop stays
non-selectable. Next selectable was the task I filed last loop.

## The "measure first" instruction paid off — it corrected my own recon TWICE
1. The recon argued goopg is on upstream's run-it side from initdb's
   `btm_version = 4`. That is the BOOTSTRAP CATALOG metapage, NOT the
   property the tier needs — which is the heap TID inside the key, a
   per-INDEX property. `var pgIndexTupleKeys = true`, so ordinary indexes
   take the TUPLE format while expression-key / explicit-opclass /
   odd-collation indexes keep BLOB. **Both arms are reachable in
   production**, which makes the gate a real branch, not a formality.
   (A comment at the `keyFmt` site claiming "blob for every index today"
   was stale and is corrected.)
2. The recon's suggested `(*nbtree.BTree).Search(entry.Key)` would have been
   the exact misuse `pgindex_btree.go` warns about: a stored ENTRY key
   carries the real TID, a PROBE key carries zero, and mixing them is "a
   silent under-read, not a failure". The tier descends over PageSource
   COPIES instead, mirroring `_bt_search` + `_bt_binsrch_insert` +
   `_bt_compare == 0`.

## Live verification caught a defect no unit test could reach
The 0A000 refusal first surfaced as `XX000: 0A000: cannot verify ...` — the
right SQLSTATE stringified INTO the message — because `btIndexCheck`
re-wrapped every tier error as internal. That wrap happens only at the SQL
boundary, so only running the real thing could see it. `btIndexCheck` now
returns an `*ExecError` unchanged and wraps only genuine read errors.

## Verified live (both arms + the br expectation)
- tuple-format `rd_a`, `rootdescend := true` → clean
- blob-format `rd_expr` on `(a+1)` → `ERROR: 0A000: cannot verify that
  tuples from index "rd_expr" can each be found by an independent index
  search`
- `pg_amcheck --heapallindexed --rootdescend` → exit 0 (br slice's
  recorded expectation still holds)

## Detection test meets the bar this task sets
Two leaf pages swapped THROUGH the PageSource, not by editing item bytes:
every page stays individually valid (per-page tier finds nothing) and only
the search-path→holding-page mapping breaks. Non-vacuity verified — stubbing
the descent to "found" fails exactly that arm. The healthy arm doubles as
the routing check.

## Gates (all green)
units; whole `TestPort_PgAmcheck*` family; amcheck + nbtree suites;
tpch-spotcheck Q12=2/Q13=33; tpcds-sf025 `PLAN-SHAPE same=99 changed=0`;
acceptance arm 24/24; pgbench smoke.

## Deferred + ledgered
Per-entry descent is O(entries x height) — cache the root-to-leaf path or
descend once per leaf boundary; MEASURE before optimising (amcheck is
operator-invoked, and upstream gates rootdescend behind parent-check for
exactly this reason). Posting-list handling compares the TID explicitly
rather than via upstream's `postingoff` test — equivalent, less specific.

## Next loop
M0119 has only the umbrella M0119-0006 left (living/seeded). Then the
banner's M0122 → M0131 → M0134 → M0135/M0136 → M0095/M0110. Check the
nightly run id first — a new one unblocks PgoutputInterop, whose inlined
cluster.log tail is now the evidence to read.

## Owner escalations OPEN — three
1. M0145-0018 cost-model no-go. 2. M0145-0001 lineage exhausted. 3.
partition_aggregate's inventory row marks a never-passing case must-pass.
