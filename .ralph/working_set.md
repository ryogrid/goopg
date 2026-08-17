# Working set — M0134-0005c landed; Bucket 4 is CLOSED as a line driver

**Task:** M0134-0005 (`constraints.sql`) — **M0134-0005c landed**. Sub-item `[x]`,
parent case stays `[ ]`. Selected per the Current Priority banner (M0134 next after
M-NIGHTLY). M-NIGHTLY drained: `ci/logs/action-items.md` still at run
`20260818-005518`, **items: 0** — nothing to file.

**The headline is a refuted hypothesis.** Loop #18's §10.4 guess ("the build scan
counts dead row versions") was **REFUTED by direct probe** — a plain committed UPDATE
leaving superseded versions behind provokes nothing. The real trigger is the
**preceding `BEGIN; UPDATE …; ROLLBACK;`** block. Three DDL bulk scans in
`internal/executor/operators_ddl.go` decided liveness structurally
(`Xmin==Invalid ⇒ dead`, `Xmax!=Invalid ⇒ dead`), never consulting abort status, so
after an aborted UPDATE they got liveness wrong **in both directions**: the real row
(aborted xmax) dropped, the phantom `i=1` (aborted xmin) kept. Fix: `collectBTreeEntries`
(~:11890), `forEachLiveRow` (~:11261), `validateFKConstraintExistingRows` (~:11396) now
call the **pre-existing** `isLiveForUniqueCheck` (`operators_storage.go:8767-8832`) —
no new predicate, no ctx threading.

**Two things worth not re-deriving:**
1. **Twice now the doc's own guess was wrong** (0005b: Bucket 4 mis-sized as a
   milestone; 0005c: dead-versions hypothesis). **Probe empirically before briefing.**
   The brief that demanded a live-server probe is what caught it.
2. **`ADD CONSTRAINT … UNIQUE` and `CREATE UNIQUE INDEX` are ONE path** (both via
   `bulkBuildBTreeFull` → `collectBTreeEntries`) — measured, not assumed.
   `backfillBTree` (~:12040) keeps the inverted check but has **zero callers** (ledgered).

**Measured:** 1299 → **1251** lines (−48), hunks 36 → **35**; the
`could not create unique index`/`is duplicated` error class is **gone from the diff
entirely**. `timeout 300 scripts/pg-regress-runner.sh --verbose constraints`; artifact
`tmp/regress-diffs/constraints.diff`. **Never compare to a pre-2026-08-18 number.**

**Next step:** **Bucket 4 is no longer this file's dominant line driver** — size the
next slice from the hunk classification, not the residual count. Cheapest next:
research slices 3 and 4 (`UNIQUE ENFORCED` grammar rejection; `ALTER CONSTRAINT …
ENFORCED` contype gate) — **slice 4's block on §10.4 is now lifted**. Then Bucket 3's
NOT NULL inheritance leftover, CHECK-constraint inheritance naming, and COPY FROM not
rejecting bad rows. **Bucket 5 (GiST `circle_ops`) is a real milestone; do not brief
Bucket 7.**

**Gates run:** 3 new guards 3/3 PASS (all FAIL-pre/PASS-post, proven by stashing only
`operators_ddl.go`); `go test ./internal/executor/...` PASS;
`RALPH_PRECOMMIT_SCOPE=units scripts/ralph-precommit-test.sh` PASS;
`scripts/tpch-spotcheck.sh` PASS (Q12=2, Q13=35 — Rule #1). Note `cmd/goopg` +
`internal/initdb` ran cold (cache miss, not a regression signal).

**Delegation:** `tmp/ralph-handoffs/m0134-0005c-index-build-liveness/` (researcher
`a9ae16725b1e263b7`, DONE — the probe report is the reason this loop found the real
cause); `tmp/ralph-handoffs/m0134-0005c-s1-ddl-scan-liveness/` (implementer
`ae8d79a83349fcea2`, 1 round DONE, no deviations; testers `a10b9a35c7f808053` gates,
`a9ee31b7f6355cd1f` re-measure).

**In-flight:** none.
