Task: M0110-0003 amcheck (SQL surface still BLOCKED on foreign gen-column WIP) +
NEW M0110-0007 (btree split prev-link bug discovered this loop).

DISPOSITION loop #29 (2026-06-14): did NOT rubber-stamp BLOCKED. The amcheck
engine is logic-complete (heap + B-tree), so I added the engine's missing
real-producer validation — and it found a real bug.

WHAT LANDED (uncontaminated, new test file only):
- `internal/amcheck/verify_nbtree_realtree_test.go` — builds LIVE multi-level
  B-trees via real btree.Create/Insert/split and runs every engine tier
  (per-page, item-order, cross-level downlinks, sibling-link walk, heapallindexed
  round-trip) over real on-disk pages. Symmetric counterpart to loop #64's heap
  verify_heapam_realpage_test.go.
- Sorted int4/int8/varchar trees → ALL tiers silent (validates engine decode vs
  goopg's real layout, incl. variable-length opaque high keys).
- Design doc 0110-0005 extended ("Real-producer B-tree validation"); fix_plan
  progress note + new item M0110-0007; deferral ledger appended.

BUG FOUND (filed M0110-0007, NOT fixed this loop):
- goopg `splitAndInsert` (internal/access/btree/btree.go ~L1454-1466 / L1522)
  never updates the OLD right sibling's btpo_prev on a non-rightmost split → stale
  left-link on any non-append insert pattern (random PK/UUID/secondary index).
  Only manifests on MIDDLE splits (sorted inserts split only rightmost → no
  sibling → invisible; that's why only the shuffled test trips it).
- btpo_prev is load-bearing: btree_vacuum.go reads op.Prev + WAL-logs
  RightSibNewPrev to relink siblings on page deletion. Real correctness gap.
- Fix is a WAL/concurrency change (fold old right sibling into atomic split WAL
  record + replay + Lehman-Yao lock order); needs -race + recovery gates → its own
  bounded loop, NOT a blind engine-loop change. internal/access/btree is CLEAN.
- TestVerifyBtreeEngineDetectsStaleSiblingLinkOnRealTree pins current behaviour as
  a DETECTION assertion; flip to silence when M0110-0007 lands.

Gates run: go test -race ./internal/amcheck (PASS); go build ./... (clean);
go vet ./internal/amcheck ./internal/access/btree (clean); ralph-state-guard
(before status block).

Contamination UNCHANGED: foreign gen-column WIP frozen 2026-06-13 14:28 (~28h);
catalog/parser/planner/executor/analyzer/mvcc + server/dispatch dirty + 2
untracked gen_override tests. HUMAN must clear it.

Next step (priority order):
1. M0110-0007: dedicated bounded btree/WAL loop on the CLEAN internal/access/btree
   package — update old right sibling prev-link on split + atomic WAL + replay +
   race/recovery gates; flip the detection test to silence.
2. After human clears tree: amcheck SQL surface S1/S2 (0110-0008) over finished
   engine → port 002_nonesuch → flip CSV AC-002→port.
3. CREATE TABLESPACE DDL (M0095-0003/011); pg_dump 002+ catalog parity (M0110-0001).
