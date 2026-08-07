Task: M-NIGHTLY — `pageHasSpaceFor` and `insertItemSorted` disagree about
  btree page space (root-0040 trigger). FIXED.

Files:
  - internal/access/btree/btree.go: `itemEncodedSize` (shared budget calc),
    `insertItemSorted` → `(int, error)`, `mustInsertItemSorted` (panicking
    wrapper), `insertItemSortedVerified` → `(int, error)`, error routing in
    `tryInsertNoSplit` / `tryInsertOnCachedRightmost` / `insertIntoBlock`
    no-split branch. Safe callers (dedup-recovery, split fills, createNewRoot,
    ApplyInsertRecord) use `mustInsertItemSorted`.

Key symbols: itemEncodedSize, insertItemSorted, mustInsertItemSorted,
  insertItemSortedVerified, pageHasSpaceFor, compactRawSize

Hypothesis/Findings:
  - Root cause: two independent expressions computed the page budget
    (pageHasSpaceFor: itemIDSize+itemPrefixSize+len(key);
    compactRawSize: itemIDSize+8+len(key)). Now unified via itemEncodedSize.
  - Defense: insertItemSorted returns error instead of panicking on
    ErrNoSpaceInPage; fast-path callers route to split path.
  - The theoretical disagreement was confirmed in production (root-0040) but
    the exact numeric trigger could not be reproduced in isolation — the fix
    makes the code robust against any future recurrence.

Next step: Pick next M-NIGHTLY item. Leading candidates:
  (a) Merge m018 to master (all milestone work landed, clean checkpoint)
  (b) regress diff capture (nightly discards diffs → unactionable)
  (c) regress/suite-wedge — aggregates/jsonb/misc 120s timeout
  (d) M0123 pg_node_tree serialization (branch wal-pg-nodetree, major infra)

Gates run: btree tests PASS (2.4s), pre-commit units PASS (42/42),
  ralph-state-guard PASS (auto-repaired)

In-flight: none
