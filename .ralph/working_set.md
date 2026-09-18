Task: M0140-0006a — expose each UNION ALL branch's searched RelOptInfo
onto the SETOP rel (`.ralph/fix_plan.md:2606`, banner item 5). DONE and
committed this loop (`3f80f802a`).

Banner item 5 reads "M0140-0006a → 0006b → 0006c" — **M0140-0006b is next**
(`.ralph/fix_plan.md:2620`, "the partial-Append cost producer") but MUST
NOT go live (be reachable by any gate) ahead of M0140-0006c (the executor
claim-set fix for `setOp` under `Gather` — `parallel_scan.go`-style claim
logic doesn't exist for `setOp`, so wrapping it in `Gather` today silently
duplicates every row). Read
`docs/design/0100-0149/m0140-0006-decomposition-into-a-b-c.md` and this
loop's `docs/design/0100-0149/m0140-0006a-expose-setop-branch-partialpathlist.md`
before starting 0006b.

Files this loop: `internal/optimizer/path.go` (new `RelOptInfo.LeftBranchRel`/
`RightBranchRel *RelOptInfo` fields, mirrors `SearchCandidates`'s doc-comment
convention), `internal/optimizer/windowsetoppaths.go` (`createSetOpPaths`
now sets both from `searchedRelOf(setOpNode.Left/.Right)` right after
building `lseed`/`rseed`), `internal/optimizer/windowsetoppaths_test.go`
(2 new tests). Design doc:
`docs/design/0100-0149/m0140-0006a-expose-setop-branch-partialpathlist.md`
(new, indexed in `docs/design/README.md`). `.ralph/fix_plan.md`
(M0140-0006a ticked `[x]`).

Key symbols: `searchedRelOf(n Node) *RelOptInfo` (`searchedtree.go:169`,
pre-existing since 2026-09-09 — the M0140-0004 recon never referenced it).
`RelOptInfo.LeftBranchRel`/`RightBranchRel` (new). `createSetOpPaths`
(`windowsetoppaths.go:338`).

Finding: the M0140-0004 recon's "no channel exists" premise was STALE —
`searchedRelOf`/`searchedTree.searchRel` (R21 slice 2a/2b, `6a51087fb`/
`6302fb8d6`, 2026-09-09) already carries a search root's own `RelOptInfo`
(PartialPathlist included) through to any finished Node reachable via a
single-child pass-through chain, and `createOrderedPaths` already uses it
(M0141-S2b-2a). A SetOp branch is planned by the identical
`planSelectWithSettings` recursion, so the real gap was just "nothing in
`createSetOpPaths` calls `searchedRelOf`" — a 2-line fix, not new plumbing
through `planner.go`. `addSetOpPaths` untouched, so no plan can move.

Gates run: `go build ./...` clean. `go vet ./internal/optimizer/...`
clean. `go test ./internal/optimizer/...` PASS (full package, no
`-count=1`, includes the 2 new tests). `go test ./internal/executor/...`
PASS (sibling-path audit — no executor change needed). `scripts/tpch-spotcheck.sh`
PASS (Q12=2/Q13=34) against the staged tree. `scripts/tpcds-sf025-regression.sh
sweep` PASS=96 MISMATCH=0 CKMISMATCH=0 ERROR=0 TIMEOUT=0, PLAN-SHAPE
queries=99 same=99 changed=0, against the staged tree. `scripts/tpch-acceptance-arm.sh`
PGSHAPED=1 HEAD baseline (`459e4e30d`, via a `git stash push -- <3 files>`/
build/`git stash pop`/re-`git add` round-trip, private port 5583) vs this
staged tree: VERDICT PASS, 24/24 labels MATCH. **Gotcha hit and resolved**:
the FIRST acceptance-arm attempt used the script's own `PGSHAPED=0` default
and got a `BOTH-ERROR` on Q9 (identical 600s-timeout on both arms, a known
pre-existing artifact unrelated to this change — re-running with
`PGSHAPED=1` to match `tpch-spotcheck`'s actual production default cleared
it). All three gate stamps' `code_tree` verified to match
`sha256(git ls-files -s -- internal cmd go.mod go.sum)` of the committed
index before committing. `python3 scripts/ralph_protected_regions.py
check-designdocs` exit 0. `python3 scripts/ralph-lineage-guard.py` clean.
`make ralph-state-guard`: same status/progress clean-exit-marker
inconsistency the last several loops also hit, auto-repaired, then clean.
commit-msg hook (G2, RALPH_LOOP=1) required a `PARITY: N/A — <reason>` body
line with a real em-dash (`—`), not `--` — first commit attempt was
rejected on that alone; re-ran with the correct character.

Learning for next loop (M0140-0006b): it can read
`setOpRel.LeftBranchRel.PartialPathlist` / `.RightBranchRel.PartialPathlist`
directly — no further plumbing needed. It needs its own `cost_append`
partial-path arithmetic (PG oracle `costsize.c:2250`, already cited by
`windowsetoppaths.go:337-339`'s serial arm) and must land GATED (behind
`gatherPathsMode`, same as K80) and NOT be exercised by any gate that could
select it until M0140-0006c (executor claim-set) also lands — see the
parent decomposition doc's ordering constraint. Re-measure Q5/Q76 (and
Q2/Q14/Q71/Q75 per the six-query denominator note in that doc) via
`scripts/tpcds-sf025-regression.sh` once it's built.

In-flight: none. Both private-lane arm binaries/runners (port 5583,
`tmp/goopg-acceptance-{baseline,mine}-bin` and their runner counterparts)
and the `/tmp/arm-m0140-0006a-*.txt` digest files deleted after the diff;
port 5583 verified free (`ss -ltnp`) before finishing. No shared cluster
(`:65432`/`:65433`/`:65437`/`:65438`) was started, stopped, reset, or
written beyond the online `pg_basebackup -X fetch` clone source reads the
private-clone scripts already do.
