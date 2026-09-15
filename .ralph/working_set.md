Task: M0140-0006 — banner item 3's last piece. **DONE this loop, as a
decomposition, not an implementation.** M0138-0007/0008/0009 were already
`[x]` before this loop (confirmed by grep), so item 3's only remaining work
was M0140-0006. Re-grounded the M0140-0004 recon (which this same milestone
already deferred once) against current code and reconfirmed its own sizing
claim before attempting anything: landing a real partial-Append producer
needs three structurally independent pieces, each comparable to a whole
prior C-19-series slice — too large for one loop, and the shared SetOp-
branch fold is dense with precedence-correctness invariants a blind edit
could silently break across every SetOp query in both TPC-H and TPC-DS, not
just Q5/Q76. Split it into three loop-sized sub-tasks instead of attempting
one blind.

Files: `.ralph/fix_plan.md` (banner item-3 RESOLVED note; M0140-0006 marked
`[x]` with a decomposition note; three new `[ ]` sub-tasks added —
M0140-0006a/0006b/0006c), `.ralph/deferral_ledger.md` (new row `m0140-0006`,
status `-`), `docs/design/0100-0149/m0140-0006-decomposition-into-a-b-c.md`
(new), `docs/design/README.md` (+1 row). No production Go code touched
(planning/filing-only task, matching the M0139-0007 precedent for an
oversized parent task).

Key symbols/tools (grounded, not re-derived from the stale recon): `planner.go:1106`
`planSegment`, `:1114` the `planSelectWithSettings` call that collapses a
branch to a finished `Node`, `:1120-1208` the `applySetOp`/`foldSetOpRange`
fold (carries `M0125-0016`'s left-associative INTERSECT/UNION/EXCEPT
precedence climb — the exact function 0006a must touch);
`windowsetoppaths.go:258-293` `createSetOpPaths`, `:298` `seedPathForNode`'s
opaque-`Node` wrap; `joinpathsparallel.go:82` `addPartialHashJoinPath` (the
shape 0006b should mirror); `operators_gather.go` `gatherOp` (generic
per-worker full-subtree copy) vs `parallel_scan.go`'s `ParallelGroup`/
`claimed()`/`claimLeaf`/`parallelClaimSet` (the claim-set pattern
`operators_setop.go:32` `newSetOp` needs before 0006b's flag can go live —
0006c).

Findings: confirmed via `mcp__serena__find_symbol` that
`createSetOpPaths`'s body (`windowsetoppaths.go:258-293`) is byte-identical
in shape to what the M0140-0004 recon described — no drift since that recon,
so this loop's contribution is genuinely new (the split), not a restatement.
The three sub-tasks are NOT independent of each other in landing order:
0006a is pure plumbing (safe alone, must be shape-neutral — TPC-H
`match=8`/TPC-DS `match=2` byte-identical is 0006a's own acceptance bar);
0006b depends on 0006a; 0006c is independent of both but is a hard
correctness gate on 0006b — 0006b's flag must never go live (default-on or
exercised by a gate that could select it) until 0006c exists, or a Gather
over `setOp` silently duplicates every row.

In-flight: none. No server/gate process left running.

Next step: per the banner (re-read this loop, item 3 now closed), item 4 is
next — M0142-0004 first inside it (a re-measurement of TPC-DS row-estimate
error size, needed before M0141/M0142's remaining slices can be judged).
M0140-0006a is also a reasonable pick if a future loop judges it higher
value (item 3's own new sub-tasks aren't explicitly re-ranked by the banner
text beyond "closed"), but M0142-0004 is the banner's literal next item and
should be preferred absent a reason to deviate. Read
`docs/design/0100-0149/m0140-0006-decomposition-into-a-b-c.md` before
picking up any of 0006a/0006b/0006c — it has the acceptance bar and landing
order.

Gates run: `make ralph-state-guard` — one self-repair (the same recurring
benign stale-clean-exit-marker pattern several prior loops have noted),
clean after repair. Pre-commit hook's pgbench smoke will run at commit time.
No `go test`/`tpch-spotcheck.sh`/TPC-DS sweep run this loop — no production
Go code changed (planning/filing-only), matching the precedent set by
M0137-0020 and M0139-0007's own recon-and-split closures.
