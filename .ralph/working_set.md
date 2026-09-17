Task: M0141-S7-cd-q64 (recon, `.ralph/fix_plan.md` item 4's "cost diagnosis
only" scope, selected via banner's P0-E6-wait fallback: M0143 has only the
owner-gated M0143-0007b left, so fell through to "recon-only tasks from items
3-6" -> M0141-S7's two open cd-* recon tasks -> cd-q64 (listed first).
DONE and committed this loop.

Files: `internal/optimizer/pathtrace.go` (2 new trace helpers,
`traceOrderedCandidatePopulation` + `traceIncrementalSortCandidate`, both
gated on existing `pathTraceEnabled`/`GOOPG_PGSHAPED_DP_TRACE`), 
`internal/optimizer/upperordered.go` (1 call site in `createOrderedPaths`),
`internal/optimizer/incrementalsortpaths.go` (1 call site in
`addIncrementalSortPaths`'s loop), `internal/optimizer/upperordered_test.go`
(fixed `dppathLines` — it matched bare "DPPATH " prefix, which now also
matches the 2 new record kinds; narrowed to "DPPATH path "/"DPPATH partial "
only), `docs/design/0100-0149/m0141-s7-readjudicate-and-scope-incremental-sort.md`
+ `docs/design/README.md` (Update 2026-09-18b), `.ralph/fix_plan.md`
(M0141-S7-cd-q64 ticked + follow-up M0141-S7-cd-q64-reclassify filed).

Key symbols: `createOrderedPaths` (upperordered.go:63), `searchedRelOf`
(searchedtree.go:169), `addIncrementalSortPaths` (incrementalsortpaths.go:149).

Findings: traced the REAL Q64 (not a synthetic repro — loaded actual SF0.25
TPC-DS data into a private port-5533 cluster since the RALPH_LOOP guard now
blocks restarting shared `:65437`). Both of the prior loop's hypotheses were
WRONG: `searchedRelOf(input)` returns non-nil (`searchedrel=true`),
`SearchCandidates` has 7 entries, 6 with non-empty validated keys. All 6 are
`PathMergeJoin` (kind=4) with `ncommon=0` against the outer ORDER BY — zero
shared columns, not a partial-prefix miss. Root cause: Q64's outer `ORDER BY`
(`cs1.product_name, cs1.store_name, cs2.cnt, cs1.s1, cs2.s1`) sorts on the
CTE `cross_sales`'s own AGGREGATE OUTPUT columns — no join-shaped candidate
can ever carry a prefix of that. PG's own plan satisfies the outer level with
a plain `Sort`; the ONE `Incremental Sort` node in PG's Q64 plan
(`Presorted Key: item.i_item_sk`) is INSIDE the CTE's own `GroupAggregate`
build — a completely different call site than `createOrderedPaths` (which
only ever sees the statement's OUTER `ORDER BY`). Q64 was miscategorized in
the original 14-witness census, not under-served by a gap at this call site.

Next step: **M0141-S7-cd-q64-reclassify** (fix_plan.md, just filed) — confirm
whether `electOrderedGrouping`'s scope already covers a CTE's own internal
aggregate-strategy election, or whether that's an un-audited path; correct
the "Nested Loop x4" tally to x3 (Q4/Q11/Q35) either way. After that (or if
skipped), re-check the banner: M0141-S7's other open recon task,
**M0141-S7-cd-candidatepool** (7-witness cost-gap investigation, unaffected
by this loop's Q64 finding — Q64 was never one of its 7), is next in file
order under item 4. If both close, item 3's leftover implementation tasks
(M0141-S2a-fix1-sweep-a/b, M0141-S2b-6-resume, M0139-0007c) are NOT
selectable while P0-E6 waits (recon-only restriction) — re-check whether
M0143-0007b's owner-decision gate has been resolved, else fall to M-NIGHTLY.

Gates run: `go build ./...` clean; `go vet ./internal/optimizer/` clean;
`go test ./internal/optimizer/...` PASS (caught+fixed the `dppathLines`
collision itself); `RALPH_PRECOMMIT_SCOPE=units scripts/ralph-precommit-test.sh`
all PASS; `make ralph-state-guard` auto-repaired a stale completed-marker,
clean after. No TPC-H dependency (TPC-DS-only recon). `:65437`/`:65438`
verified untouched (`bench/tpcds/server.sh status` before/after); private
`tmp/m0141s7cdq64/` cluster stopped and deleted before finishing.

In-flight: none.
