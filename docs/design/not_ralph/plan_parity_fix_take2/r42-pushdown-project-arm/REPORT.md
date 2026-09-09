# R42 — the qual-pushdown descent now crosses a `Project` (K76/K77)

*2026-09-09. All gates green. Read `DESIGN.md` first; §9 there records the
pre-implementation review, whose corrections changed this round's scope.*

## 1. Result

| | before | after | PG 18.3 |
|---|---|---|---|
| TPC-DS plans changed | — | **2** (Q47, Q57) | — |
| Q47 `CTE Scan on v1` | no restriction | **`Filter: ((d_year = 2000) AND (avg_monthly_sales > 0))`** | same restrictions applied at that level |
| Q57 `CTE Scan on v1` | no restriction | **`Filter: ((d_year = 2001) AND (avg_monthly_sales > 0))`** | same |
| Q47 / Q57 runtime | 5454 ms / 2592 ms | 5487 ms / 2609 ms | — |
| TPC-DS decline census | 8 | 8 | — |
| match count | 0/99, 1/22 | 0/99, 1/22 | — |

**Both changed plans gained a restriction that PG also applies at that
level** — verified on the live oracle, which renders
`Filter: ((avg_monthly_sales > '0'::numeric) AND (d_year = 2000) AND …)`
on the corresponding node. So the movement is *toward* PG on the
`qual-placement` category, which was this round's stated success criterion.

The match count and decline census did not move, **exactly as §4 predicted
before implementation**. This round was never scoped to produce a match; it
exists to unblock R41.

## 2. What landed

A `*Project` arm in `pushConjunctTraced`
(`inner_join_qual_pushdown.go`), reusing the same
`remapConjunctThroughProjection` helper `pushConjunctIntoCTEBody` already
uses, and mirroring the `*Join` arm's remap → recurse → assign shape.

Three fail-closed guards, all from review:

- **`st.proven = false`** (K77). This is the line that keeps R42
  placement-only. `proven`'s sole consumer *deletes the conjunct from the
  residual `Filter`*; leaving it intact would have newly licensed a
  residual-dropping MOVE on a whole class of trees, resting on a remap that
  still has an unnamed-ref fail-open seam. The qual is now planted below
  **and** kept above.
- **`IsolatedScope` refused**, with the verbatim precedent in
  `upper_narrow_chain.go`. Such Projects were previously contained only by
  *accident* — a view-rename Project declines merely because the view and
  body column names differ, which stops working the moment they match.
- **`len(Output()) != len(Targets)` refused**, mirroring the sibling
  `*Aggregate` arm; this closes the second fail-open seam in the remap.

## 3. Gates

| gate | result |
|---|---|
| `RALPH_PRECOMMIT_SCOPE=units` | green, 0 failures |
| optimizer + executor suites | green |
| new unit tests | 5 — one push, **four declines** (computed target, `IsolatedScope`, layout mismatch, plus a copy-not-move assertion on `proven`) |
| **TPC-DS SF0.5 sweep** | **PASS=95 MISMATCH=0 CKMISMATCH=0 ERROR=0 TIMEOUT=0** |
| TPC-DS verdicts + row counts vs HEAD | byte-identical for all 99 |
| TPC-DS plan shape vs HEAD | 2 changed (Q47, Q57), both explained above |
| TPC-DS total runtime | 838 s → 817 s |
| TPC-H values digest, 22 queries | byte-identical |
| TPC-H plan STRUCTURE, 22 queries | identical |
| decline census | unchanged (8), as predicted |

A note on the baseline: the sweep's own status-delta compared against
R41's run, which is **not** HEAD — R41 was reverted. The rows above use the
correct pre-R41 baseline (`sweep-20260909-211945`), re-diffed by hand.
Against R41's run the sweep reported Q78 as changed; against HEAD it is
not, which is right — R42 alone does not admit Q78's CTE bodies to the
search.

## 4. A tooling trap worth recording (K78)

The first version of the test file was named
`pushdown_project_arm_test.go` and **silently never ran**. Go reads a
trailing `_arm` as a **GOARCH filename constraint**, so the file landed in
`IgnoredGoFiles` and was excluded on amd64 — `go test -run …` reported
"no tests to run" and `-count=1` reported the same, which reads exactly
like a passing filter typo.

It was caught by `go list -f '{{.IgnoredGoFiles}}'`, not by the test run.
Any file whose name ends in a GOARCH or GOOS word (`_arm`, `_386`,
`_linux`, `_windows`, …) is constrained. Renamed to
`pushdown_project_crossing_test.go`.

## 5. Next: R41, then Option B

With K76 closed, R41's implementation — fully specified in
`r41-anti-leaf-coordinates/DESIGN.md` §4, already built once and verified
to pass every suite and the sweep — can be re-applied. In that order the
eligibility gain (`leaf-count` 3 → 0, declines 8 → 5) arrives **without**
the qual-placement regression that forced its revert, because the
`date_dim` restriction can now reach through the searched boundary's
`Project`.

Q78 will still not match: PG places `date_dim` **below** the anti join
(`Nested Loop Anti Join` over `Parallel Hash Join`), which needs a 3-leaf
search — R41 §6's Option B. That prediction stands and the R41 report must
keep saying so.

## 6. Filed, not fixed

`deriveConstAcrossJoinEquality` mutates the tree *before* its recursion and
nothing unwinds it on a failed descent, so a derived sibling copy can sit
below un-priced (the caller takes the `kept` branch without
`notePushedBelow`). A `*Project` arm makes deep-then-fail descents more
common, so this round **widens a pre-existing wart without creating it**.
Recorded rather than bundled: fixing it means unwinding a partial mutation,
which is its own change.
