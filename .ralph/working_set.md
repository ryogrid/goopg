# Working set — M0134-0005h landed (silent partitioned-FK integrity gap closed)

**Task:** M0134-0005 (`constraints.sql`) — **M0134-0005h LANDED**. Sub-item `[x]`,
parent case stays `[ ]`. Selected per the Current Priority banner (M0134 after
M-NIGHTLY). M-NIGHTLY drained: `ci/logs/action-items.md` still at run
`20260818-005518`, **items: 0** — nothing to file.

**What landed:** a deferred FK on a PARTITIONED child **committed** where PG raises
`23503`, and `ALTER TABLE … ADD FOREIGN KEY` over a partitioned root silently accepted
violating leaf rows. `fullTableFKCheck` (`operators_fk.go:465`) and its DDL twin
`validateFKConstraintExistingRows` (`operators_ddl.go:11406`) scanned only the table
object handed to them; a partitioned root has **zero physical blocks**, so the `NBlocks`
loop ran zero iterations and the check "passed" by scanning nothing. Both split into a
per-relation helper and now loop root + `allDescendants(im, tbl, snapDetachEpoch(ctx))`.

**Four things worth not re-learning:**
- **§14.6's sizing was too large — the THIRD time in this milestone.** It implied a
  `PartitionChildren` walk had to be written; `allDescendants` (`operators_fk.go:950`)
  already existed and was already called by two sibling scans in the SAME file.
  Probe the producer before believing any "needs new infrastructure" claim.
- **Do NOT change what is queued.** `DeferredFKCheck.ChildTableName` records the ROOT
  and that is correct; the fix belongs at scan time.
- **Resolve columns BY NAME per relation.** A partition's column order can differ from
  its root's; a root-computed positional index would be fresh silent corruption.
- **The DDL twin was live-probed before being touched** and reproduced. Had it not, it
  would have been ledgered, not edited. `cloneAndValidateAttachPartitionFKs` was probed
  and found NOT affected — recorded in the ledger so nobody re-probes it.

**Measured: 1164 lines / 33 hunks, UNCHANGED — the honest result.** This bug was found
by the 0005g probe, not by the `constraints` case, which never exercises a partitioned
deferred FK. **Never compare to a pre-2026-08-18 number.**

**Next step:** the remaining `constraints` diff is now ~33 hunks of **NOT-NULL
constraint inheritance-validate** and **`COMMENT ON CONSTRAINT … does not exist`** gaps
(per the tester's read of the regenerated artifact) — that pair is the largest remaining
line driver and the natural next slice. The older §11.4 list (CHECK-constraint
inheritance naming, COPY FROM not rejecting bad rows, Bucket 3 NOT NULL inheritance,
Bucket 5 GiST `circle_ops`) is stale relative to that reading. **Bucket 7 still has no
pinned root cause — do not brief from it.** **Probe empirically before briefing** —
this doc's own guesses have now been wrong six times.

**Gates run:** `RALPH_PRECOMMIT_SCOPE=units scripts/ralph-precommit-test.sh` PASS
(~460s; `internal/initdb` 459s / `cmd/goopg` 79s cold — input change, not a cold-cache
event); `scripts/tpch-spotcheck.sh` PASS (Q12=2, Q13=35 — Rule #1); `go build ./...`;
`go test ./internal/executor/ ./internal/catalog/` PASS;
`go test -run 'TestPort_.*(FK|Fk|Partition)' ./internal/testport/` PASS (30 subtests,
incl. 13 pre-existing FK/partition isolation specs); 3 new positive guards proven
FAIL-pre (`expected 23503 … got <nil>`) by stashing only the two production files;
pre-commit pgbench smoke PASS via the hook.

**Delegation:** `tmp/ralph-handoffs/m0134-0005h-probe/` (researcher `aa89e78962814ba74`,
DONE — confirmed the cause, refuted the sizing, found `allDescendants`);
`tmp/ralph-handoffs/m0134-0005h-s1-fk-partition-leaf-scan/` (implementer
`aba153bbe5c249e67`, 1 round, DONE — both scope items);
`tmp/ralph-handoffs/m0134-0005h-gates/` (tester `a7a043b905a41124e`, DONE).

**In-flight:** none.
