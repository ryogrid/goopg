Task: M0142-0008a-3i-plumbing-c14 — LANDED and COMMITTED this loop
(design doc §49). Root-caused and fixed the actual crash c9-c13 chased
across 5 loops: Q69 (TPC-DS) now runs clean instead of panicking.

Files this loop:
- internal/optimizer/joinsearchseam.go: `chainCarriesLateral` gained a
  Semi/Anti arm (descends `j.Left` with the function's existing coarse
  "any Lateral reachable = decline" rule, mirroring
  `extractSearchLeaves`'s own Semi/Anti descent exactly). This is the
  ENTIRE production fix — one function, ~25 lines including comment.
- internal/optimizer/chaincarrieslateral_test.go (NEW): two direct unit
  tests — a Lateral join under a Semi/Anti's Left must be caught (both
  JoinTypeSemi and JoinTypeAnti), and a non-lateral chain under a Semi
  join's Left must NOT be declined (guards against over-broad decline).
- docs/design/0100-0149/m0142-0008a-1-semi-anti-sji-design.md: new §49
  (49.1 method/root-cause trace by code-reading not re-instrumentation,
  49.2 the fix, 49.3 live verification incl. full SF0.25 sweep, 49.4
  what's still open).
- docs/design/README.md: appended a §49 summary to the m0142-0008a-1
  index row (the row is ~196 raw lines of flowing text per a known
  pre-existing broken-table-cell quirk — appended at the row's true
  physical end, matching the established per-loop convention).
- .ralph/fix_plan.md: c14 marked [x] with full outcome; c9-c13's carried
  "predp.go:159-176 stale comment" item explicitly left NOT actioned
  (reasoned, not measured, to be orthogonal — see ledger row).
- .ralph/deferral_ledger.md: new row for the still-open predp.go:159-176
  comment (carried since c11, now explicitly why-not-touched-this-loop
  too).

Key symbols: `chainCarriesLateral` (joinsearchseam.go, the fix site),
`extractSearchLeaves` (joinsearchseam.go, the function it now correctly
mirrors), `createNestLoopIndexJoinPlan` (createplannl.go, builds the
`*Join{Lateral:true}` node that was being mis-decomposed),
`runJoinSearchBelowPinned`/Phase A+B (predp.go, the two-search structure
whose interaction created the corrupted splice).

Root cause (confirmed, not just theorized): `extractSearchLeaves`'s
Semi/Anti arm (landed by b1/b2) descends a Semi/Anti join's `Left`
looking for reorderable structure. `predp.go`'s Phase A search runs on
the subtree BELOW the pinned Semi/Anti spine and splices its own
already-`createPlan`'d winning tree into that `Left` BEFORE Phase B ever
walks the spine. For Q69 that spliced tree contains a correctly-built
`*Join{Type:Inner, Lateral:true, Right: is}` (an ordinary `*Join`
struct — no marker distinguishes "already planned" from "raw AST").
Phase B's `extractSearchLeaves` walk can't tell the difference either
and decomposes this already-decided Lateral join into two independent
leaves — `is` (the outer-parameterized `*IndexScan`) becomes a
standalone plain leaf (c13's crash), and `customer`/`customer_address`
become a separate leaf orphaned from `customer_demographics` (c13's
"illegal pairing", now proven to be the SAME root cause, not a second
bug). `chainCarriesLateral` (the guard meant to catch this) was never
extended when b1/b2 added `extractSearchLeaves`'s Semi/Anti descent — it
fell to a finer-grained "does an OuterColumnRef escape unbound" check
(answers NO — `is.Key` is correctly bound) instead of its own coarse
"any Lateral reachable" rule. One new arm fixes it.

Gates run this loop: `go build ./...` clean. `go vet
./internal/optimizer/...` clean (pre-existing unrelated diagnostics
only). `go test ./internal/optimizer/...` PASS (incl. 2 new tests).
`go test ./internal/executor/...` PASS (sibling-path/practice-card
gate). Live repro: private binary (`tmp/c14-bin`) + private SF0.25 data
copy (`tmp/c14-sf025-data`), both removed after — Q69 runs clean (100
rows), EXPLAIN confirms correct plan shape. Full
`scripts/tpcds-sf025-regression.sh sweep` with `GOOPG_BIN=tmp/c14-bin`:
`PASS=96 MISMATCH=0 CKMISMATCH=0 ERROR=0 TIMEOUT=0 SKIP=3`,
`PLAN-SHAPE: same=99 changed=0` (only Q69 moved, crash→pass). `make
ralph-state-guard`: self-repaired a stale status/progress marker from
the prior loop's clean exit (same pattern noted in the previous baton),
then reported OK.

Next step: NOT YET SELECTED — this task is fully closed. Per the banner
(`fix_plan.md` §"Current Priority", item 4), the next pick should be
either another M0141/M0142 slice or, given `-3i-plumbing`'s whole c-chain
(c1-c14) is now closed with a real fix landed, re-run the
`GOOPG_PGSHAPED_DP_TRACE=1` corpus check c35/c36 used to confirm whether
`jointype=semi`/`anti` DPPATH reachability (long stuck at zero
corpus-wide) has moved now that Phase B no longer corrupts the one query
that used to reach it — that re-measurement is the natural immediate
follow-up but was NOT started this loop (one task per loop) and is not
yet filed as its own fix_plan item. A future loop should file it
explicitly before starting it. Also still open (not this loop's job):
`predp.go:159-176`'s stale comment (ledger row filed), and c9's
`joinIsLegal` zero-predicate-pairing legality question for a
DIFFERENT query than Q69 (§49.4 — Q69 no longer reaches that code path
after this fix, so it's unconfirmed either way for a query that does).

In-flight: none. Private diagnostic binary/data/log (`tmp/c14-bin`,
`tmp/c14-sf025-data`, `tmp/c14-server.log`) all removed after
verification. Unrelated pre-existing `tmp/c14-q67.*`/`tmp/goopg-c14`
files (from a different, older milestone's numbering, dated well before
this loop) were left untouched — not mine, out of scope.
