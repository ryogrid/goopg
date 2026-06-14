Task: M0110-0003 — pg_amcheck port. Loop #65 landed the heapallindexed
index-side relation walk (engine). COMMITTED this loop.

This loop (#65): added `CollectBtreeLeafEntries` + `VerifyBtreeHeapAllIndexedRelation`
in new `internal/amcheck/heapallindexed_relation.go` (+ _test.go, 10 tests),
symmetric with loop-#63 `VerifyHeapRelation`, behind the existing `PageSource`
seam. Descends metapage Root→leftmost leaf→sibling walk, collects leaf entries
(posting-expanded), composes the pure loop-#61 fingerprint+probe core. All new
files; zero contaminated files touched. gofmt/vet clean; -race PASS on
internal/amcheck + internal/access/btree; go build ./... OK.

STATE CHANGE this loop: the gen-column WIP holder (pid 2177381) is now DEAD.
Its uncommitted changes still sit in the shared parser/planner/executor/catalog
files (frozen 2026-06-13 14:28, ~25h stale). A different `claude --resume
ec98936f` session is alive but NOT editing them. The amcheck SQL surface
(CREATE EXTENSION amcheck + verify_heapam/bt_index_check SRF) edits exactly
those shared files, so it still must NOT be attempted — a HUMAN must first
clear those uncommitted changes (commit/stash/discard).

amcheck engine status: BOTH heap and B-tree engines are now logic-complete
(every tier `bt_check_every_level` / `verify_heapam` performs is ported,
relation-walk drivers present for both sides). Only remaining engine gap is the
`heapEntries` producer (heap scan + index_form_tuple via index TupleDesc —
catalog coupled, wire layer).

Next step (pick one):
- PREFERRED once tree is clean: wire the SQL surface — `CREATE EXTENSION
  amcheck` (parser + pg_extension row + pg_proc) + `verify_heapam(regclass,…)`
  SRF (uses VerifyHeapRelation) + `bt_index_check`/`bt_index_parent_check` SRF
  (uses the btree tiers + VerifyBtreeHeapAllIndexedRelation), filling PageSource
  from the smgr. Then port `002_nonesuch.pl` (AC-002, error-path only first).
- If tree still dirty: the engine is logic-complete; further engine work is
  diminishing returns. Consider M0110-0001 (pg_dump 002 — catalog-coupled, also
  contaminated) or M0110-0002 (pg_waldump 002 — needs PG-format FPI heap WAL,
  high blast radius) instead, or flag the dirty-tree blocker for a human.

Gates run: go test -race ./internal/amcheck ./internal/access/btree PASS;
go build ./... OK; gofmt+vet clean. (No TPC-H gate — no planner/executor/codec
change; all changes confined to new internal/amcheck files.)
