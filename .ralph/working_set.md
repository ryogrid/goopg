Task: M0130-S11.4 slice 3b-3c — `_bt_truncate` suffix truncation. DONE, being
committed this loop.

Landed: `indexFormat.truncateSeparator` (internal/access/btree/pgtruncate.go)
= `_bt_keep_natts` + `index_truncate_tuple`, called from the split path
(`insertIntoBlock`) and both bulk-loader levels (`buildLevel`,
`buildLevelRaw`), LEAF levels only. The split path also stopped re-deriving the
parent downlink from `rightItems[0]` — `_bt_insert_parent` builds it from the
LEFT page's high key, and 3b-3c makes those two different keys.
Key finding: `_bt_truncate`'s SECOND branch is a correctness fix, not a size
one. With every key attribute equal, a separator without `lastleft`'s heap TID
is minus infinity in the implicit last key attribute, so every left-page entry
sharing that key compares GREATER than its own page's high key. Mutation-checked
(truncation disabled): a point descent for the first of 1500 duplicates returns
the WRONG heap TID — {12,25} instead of {0,1}. Unreachable before the flip.
`indexFormat.marshal` now carries BT_PIVOT_HEAP_TID_ATTR through its natts
re-stamp (it re-stamps every pivot on the way to the page).
NOT REINDEX-required: an untruncated separator is still legal.

Guard: internal/access/btree/pgtruncate_test.go (blob no-op byte-for-byte;
keep-natts on a THREE-column desc because FormPGIndexTuple MAXALIGNs and a 2->1
cut is free; tiebreak TID/size/invariant with the pre-slice separator kept as
the mutation reference; marshal round trip; 1500-duplicate end-to-end tree).

Gates: btree/amcheck/storage PASS; units PASS; tpch-spotcheck PASS (Q12=2,
Q13=35); pgbench smoke via the commit hook. TPC-DS SF0.5 NOT run (backward
compatible, no REINDEX debt added) — run it before the next REINDEX-required
slice.

Ledger row filed: leaf items are still admitted without
`CheckPGBTItemSize(size, true)` (a separator may now need 8 more bytes) ->
3b-3d; `MaxItemsPerPage` cannot tighten on this slice's account after all (the
3b-3a resume point was wrong: negative-infinity downlinks stay zero-length);
bulk loaders skip truncation at an i==0 boundary; still no `_bt_dedup_pass`;
`keyExceedsHighKey`'s plain `compare` re-audited and found to MATCH upstream.

Next step (re-read the fix_plan banner first; the six AI-20260810-011258-*
items stay filed and unchecked per the banner):
1. **3b-3d — `MaxHighKeyLen`/`bulkHighKeyReserve` -> `BTMaxItemSize`**, which is
   also where the `CheckPGBTItemSize(size, true)` leaf bound belongs.
2. Then S11.5 (`RM_BTREE` WAL) per the fix_plan order.

In-flight: none.
