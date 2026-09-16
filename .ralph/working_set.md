Task: M0142-0008a-3i-plumbing-c20 (DONE, ready to commit) — found and fixed
the real `semianti-on-qual` decline c19's own resume point pointed at: a
coordinate-space bug in `extractSearchLeaves`'s semiAnti chain-link rebase
(latent since c6/c7, not introduced by c19 — c19 only gave the first
producer enough reachability to expose it live).

Files: internal/optimizer/joinsearchseam.go (new `remapWalkOrderFlatToSpans`
func next to `buildLeafSpans`, + a ~15-line call site right after
`cumOffsets := buildLeafSpans(...)`), internal/optimizer/semiantichain_test.go
(new `TestRemapWalkOrderFlatToSpans_RealLeafAfterSyntheticRHS`). Docs:
.ralph/fix_plan.md (c20 entry after c19), .ralph/deferral_ledger.md (c20 row),
docs/design/0100-0149/m0142-0008a-1-semi-anti-sji-design.md (new §56),
docs/design/README.md (m0142-0008a-1 row: appended c20 summary after c19's).

Key symbols: `remapWalkOrderFlatToSpans` (new), `buildLeafSpans`
(joinsearchseam.go:1561, unchanged — its out-of-band synthetic-leaf
placement was correct all along, just not consulted by the semiAnti
predicate rebase until now), `rebaseSemiAntiChainQual` (unchanged — its
walk-order-flat output was ALSO correct all along; the gap was nobody ever
converted that into `cumOffsets` space), `semiAntiOnQualsOK`
(joinsearchseam.go:1834, unchanged — it was declining CORRECTLY on bad
input, not buggy itself), `relfromjoinlist.go:654-678`'s `outer-over-derived`
firewall (pre-existing, deliberate, now the thing Q78 correctly hits next).

Findings: live-traced (temporary `GOOPG_C20DEBUG=1`, fully reverted before
commit) that the failing conjunct in `semiAntiOnQualsOK` was the FOLDED
`LeftKey=RightKey` synthetic equality (not either of `j.Predicate`'s own
2 conjuncts — refutes c19's "redundant duplicate, probably harmless" guess).
Root cause: `extractSearchLeaves` rebases a semiAnti link's pred into
WALK-ORDER flat column space, but `relidsOfExpr`/`searchConsumes` read
`buildLeafSpans`'s `cumOffsets`, which relocates every SYNTHETIC (semiAnti
RHS) leaf's span OUT-OF-BAND after the total REAL-leaf width. The two spaces
coincide only when no real leaf follows a semiAnti link's synthetic leaf in
walk order — Q78's `(websales ANTI webreturns) JOIN date_dim` shape breaks
that (date_dim, real, trails the pair). Fix: deferred remap pass, right
after `cumOffsets` is known (only then is the synthetic leaf's true
out-of-band target computable). Verified live: after the fix,
`semiAntiOnQualsOK` passes ALL conjuncts for ALL 3 of Q78's CTEs; Q78's
decline moves to a DIFFERENT, LATER, PRE-EXISTING, DELIBERATE firewall
(`outer-over-derived`, names Q78 explicitly, guards a documented
15s->327s-timeout regression, resume gated on TODO_ALL B-06's CTE-output
stats) — so Q78 correctly still declines, now for the right reason, never
touching the timeout shape.

Next step: c21 should NOT try to lift `outer-over-derived` directly (that
needs CTE-output-stats synthesis, a separate milestone-sized prerequisite
per its own doc comment). Instead pick up the STILL-UNFILED, materially
larger question flagged by both c18 and c19 and re-confirmed untouched this
loop: Q10/Q16/Q35/Q69/Q94 (the EXISTS/IN family the c1-c17 chain was BUILT
for) show ZERO semiAnti trace of any kind corpuswide — instrument
`whereEligibleForPreDPUnnest` (predp.go:35) for those 5 statements
specifically (does it decline the WHOLE statement for an unrelated
scalar-sublink reason, sending them down the legacy DP-before-unnest order
where no Semi/Anti node exists yet when the search runs) before assuming
any further DP-search-layer fix could matter for that family at all.

Gates run: `go test ./internal/optimizer/...` PASS (incl. new test),
`go test ./internal/executor/...` PASS. `scripts/tpcds-sf025-regression.sh
sweep` (private GOOPG_BIN) PASS=96 MISMATCH=0 CKMISMATCH=0 ERROR=0
TIMEOUT=0 SKIP=3, `scripts/tpcds-plan-diff.py` vs pre-fix baseline:
changed=0/99 (pure no-op on every chosen plan — Q78 still declines overall,
just via a different, correct gate). `tpch-spotcheck.sh` SKIPPED
(pre-existing M0142-0003k data-reload blocker, unrelated). `make
ralph-state-guard` self-repaired a stale running/completed mismatch, passed.
`go build ./...` clean. `gofmt` diff on both touched files shows ONLY
pre-existing, unrelated skew (go1.26.3 local vs go1.25 repo baseline,
per-project convention: never `gofmt -w` wholesale) — confirmed by diffing
gofmt's output against the committed file and checking every hunk predates
this loop's own edits.

In-flight: none. All temporary `GOOPG_C20DEBUG=1` trace instrumentation
(fmt.Fprintf/os imports in joinsearchseam.go, added during investigation)
was reverted before the final build; the committed diff is the real fix
only (2 files: joinsearchseam.go +52/-0, semiantichain_test.go +71/-0) plus
docs. Scratch binaries (tmp/goopg-c20-scratch-bin, tmp/goopg-c20-bin) and
scratch logs (/tmp/c20-*.log, /tmp/c20-*.txt) removed. The private SF0.25
server this loop started/stopped (port 65437, GOOPG_CG_UNIT=goopg-c20-*) is
fully stopped — confirmed via `ps aux` and `systemctl --user list-units`,
zero leaked processes. Pre-existing, unrelated `tmp/c20a`/`tmp/c20b-*`
scratch files (dated 2026-09-07/2026-09-16, a different task's shorthand,
NOT created this loop) were found but left untouched — not mine to clean up.
