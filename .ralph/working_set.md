Task: M0142-0008a-3i-plumbing-b2 — design doc §31.4 step (i) landed: the
three-check preamble fix in `tryPGShapedJoinSearch`, gated by production's
`admitSemiAnti=false` staying unchanged (fully inert). Step (ii) — the
`predp.go` reachability wiring — is the next selectable item under this task.

Files this loop:
- internal/optimizer/joinsearchseam.go: leaf-count check now
  `len(scans) != nprefix+len(semiAnti)`; new function
  `pgShapedOffsetChecksOK` (next to `buildLeafSpans`) replaces the old
  per-index offset-agreement loop + spine-offset check with
  synthetic-leaf-aware versions; `extractSearchLeaves`'s discarded 5th
  return is now captured as `semiAnti` and threaded through.
- internal/optimizer/semiantichain_test.go: 3 new tests —
  `TestPgShapedOffsetChecksOK_ReducesToPlainChecksWhenNoSemiAnti`,
  `TestPgShapedOffsetChecksOK_RealLeafAfterSynthetic`,
  `TestPgShapedOffsetChecksOK_SyntheticLastInWalkOrder`.
- docs/design/0100-0149/m0142-0008a-1-semi-anti-sji-design.md: §31.4 extended
  with the landing summary.
- docs/design/README.md: m0142-0008a-1 index row extended.
- .ralph/fix_plan.md: -3i-plumbing-b2's entry extended with the step (i)
  landing + step (ii) still-open note.

Key symbols:
- `tryPGShapedJoinSearch` (joinsearchseam.go:216) — production call site,
  `admitSemiAnti` literal at `:309` UNCHANGED (`false`).
- `pgShapedOffsetChecksOK` (joinsearchseam.go, right after `buildLeafSpans`)
  — new, `(cumOffsets []leafSpan, semiAnti []semiAntiChainLink, widths []int,
  bindingOffsets []int, hasSpine bool, spineOffset int) (declineReason
  string, ok bool)`.
- `buildLeafSpans` (joinsearchseam.go:1316) — unchanged this loop, already
  places synthetic leaves' spans out-of-band after the real total width.

Hypothesis/Findings: none new — this loop implemented design doc §31.3's
already-settled three-check fix exactly as specified; no live tracing done.
Ruled out: nothing to rule out, this was a pure "code the settled design"
loop per §31.4's own instruction not to collapse step (i) and step (ii).

Next step: step (ii) — extend `predp.go`'s descend loop (currently hard-bails
on any `*Join` node, `predp.go:96-101`) to pass through non-Semi/Anti `*Join`
nodes and feed the wider spine+origChain tree to `tryJoinSearch`, per design
doc §22.3/§24's item 6a (plumb a `*resolveContext` through
`unnestSubqueriesInPlan`/`unnestExistsExpr`, 7 signatures in unnest.go,
already scoped as mechanical) and item 6b (now unblocked by this loop's
preamble fix). Flip `admitSemiAnti=true` at the one production call site only
once 6a/6b are both wired, verified against a live end-to-end fixture (Q69
witness class) plus the TPC-DS SF0.25 sweep. This is the higher-blast-radius
live-DP-routing change §28.3/§29/§31.4 already flagged as needing its own
loop — do not attempt it inside a loop that also touches something else.

Gates run this loop: `go build ./...` clean. `go vet ./internal/optimizer/...`
clean. `go test ./internal/optimizer/...` full package pass (includes the 3
new tests + all pre-existing -3i-plumbing-b1/§25-29 tests). TPC-DS SF0.25
sweep: `PASS=96 MISMATCH=0 CKMISMATCH=0 ERROR=0`, `PLAN-SHAPE: same=99
changed=0` (confirms full inertness vs the pre-change commit). Units
precommit gate (`RALPH_PRECOMMIT_SCOPE=units scripts/ralph-precommit-test.sh`):
only the pre-existing unrelated `internal/parser` `GroupedJoinUnaliased`
AST-drift failure, unchanged. TPC-H spotcheck: SKIPPED (tpch schema not
loaded on `:65433` — still the M0142-0003k-documented blocker, unrelated to
this change; not a planner/executor gate this change needed since it's a
non-TPC-H-touching, fully-inert optimizer-internal change). `make
ralph-state-guard`: found and auto-repaired the same benign prior-loop
clean-exit marker several prior loops have also seen; consistent after
repair.
Nightly triage: `ci/logs/action-items.md` run 20260916-035206 (13 items) was
already filed by a prior loop (visible in fix_plan.md's M-NIGHTLY section) —
no new run this loop, nothing further to file.

In-flight: none. Nothing outstanding once this loop's diff is committed.
