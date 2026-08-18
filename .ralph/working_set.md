# Working set — M0134-0005g landed; 0005h filed (partitioned deferred FK, SILENT gap)

**Task:** M0134-0005 (`constraints.sql`) — **M0134-0005g LANDED**. Sub-item `[x]`,
parent case stays `[ ]`. Selected per the Current Priority banner (M0134 next after
M-NIGHTLY). M-NIGHTLY drained: `ci/logs/action-items.md` still at run
`20260818-005518`, **items: 0** — nothing to file.

**What landed:** named `SET CONSTRAINTS <parent> DEFERRED` never reached a partition's
cloned UNIQUE/PK constraint (session map is keyed by the typed PARENT name; the clone is
auto-named differently, so the lookup missed and fell back to `InitiallyDeferred`=false).
Both clone sites — `internal/executor/operators_ddl.go` ~:4615 (`PARTITION OF`) and
~:8402 (`ATTACH PARTITION`), the SAME Rule-#2 twin pair as 0005f — now set
`PartitionParentOID` + `RegisterIndexPartitionChild`; `uniqueCheckDeferToCommit`
(`deferred_unique.go:66`) tries its own name first, then walks child→root bounded by
`maxPartitionParentWalk = 64`.

**Three things worth not re-learning:**
- **§13.4's sizing was WRONG and the probe caught it before any code.** It claimed a
  constraint-hierarchy linkage "does not exist anywhere in goopg". `PartitionParentOID`
  existed (`catalog.go:1818`) and was already wired at 2 of 4 sites. §13.4 sized by
  reading the *consumer* + PG oracle and inferring the producer was missing.
  **Probe the producer before believing any "needs new infrastructure" claim.**
- **A post-fix-only measurement cannot attribute a metric change.** The tester first
  concluded from one post-fix reading that an earlier slice had cleared the hunk; the
  stash-and-re-measure counterfactual showed 5 → 0 from THIS slice.
- **Do NOT port PG's by-name `SET CONSTRAINTS` scan.** PG reuses the same `conname` on a
  partition's clone; goopg does not, so a name scan finds nothing. The OID walk is the
  correct goopg adaptation. (goopg's differing clone name is itself ledgered.)

**Measured:** `parted_uniq` matches **5 → 0**; aggregate **1164 lines / 33 hunks,
UNCHANGED** both before and after. Artifact `tmp/regress-diffs/constraints.diff`.
**Never compare to a pre-2026-08-18 number.**

**Next step:** **M0134-0005h** — deferred FK checks on partitioned tables silently
commit a violated FK where PG raises 23503 (`operators_fk.go:430`/`:462` scan the
storage-less partitioned root; fix = walk leaves via `im.PartitionChildren`). Prefer it
over the remaining §11.4 list (CHECK-constraint inheritance naming, COPY FROM not
rejecting bad rows, Bucket 3 NOT NULL inheritance, Bucket 5 GiST `circle_ops`) because
it is a **silent wrong-answer** bug, not a message divergence. **Bucket 7 still has no
pinned root cause — do not brief from it.** **Probe empirically before briefing** — this
doc's own guesses have now been wrong five times.

**Gates run:** `RALPH_PRECOMMIT_SCOPE=units scripts/ralph-precommit-test.sh` PASS
(`internal/initdb` 448s / `cmd/goopg` 81s cold — input change, not a cold-cache event);
`scripts/tpch-spotcheck.sh` PASS (Q12=2, Q13=35 — Rule #1); `go build ./...`,
`internal/executor` + `internal/catalog` PASS; `TestPort_Partitioned` 5/5 PASS with the
named-form guard FAIL-pre proven by reverting only the two production files; pre-commit
pgbench smoke PASS via the hook.

**Delegation:** `tmp/ralph-handoffs/m0134-0005g-probe/` (researcher `a8959a306f0f481c5`,
DONE — refuted the sizing, found the FK gap);
`tmp/ralph-handoffs/m0134-0005g-s1-named-setconstraints-partition/` (implementer
`a04929ecede741978`, 1 round, DONE); tester `a8f5ff87aaab9002f` (gates + the
counterfactual re-measure; self-corrected its first attribution).

**In-flight:** none.
