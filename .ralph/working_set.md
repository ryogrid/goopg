Task: M0142-0001 — "entry recon: is the pricing blockage still the same one?"
for the plan-parity M0142 group. **DONE, committed this loop** (branch
`plan-parity-with-pg-take2-ralph`, commit `60b2b2c40`). Also closed
M0142-0002 in the same commit (folded — same report answers it). No
production behavior change (pure recon, live re-capture only).

Files: `docs/design/0100-0149/m0142-0001-entry-recon-pricing-blockage-post-m0138.md`
(new design doc), `docs/design/README.md` (+index row), `.ralph/fix_plan.md`
(M0142-0001/-0002 checked off with findings; filed **M0142-0003a/-0003b** as
the real resume points, replacing the old placeholder M0142-0003 line),
`.ralph/deferral_ledger.md` (+1 row, task-id `m0142-0001`). Scratch artefacts
`analysis/m0142/m0142-0001-*` left untracked (same precedent as
M0138-0001/-0005/-0006, M0141-S2a) — findings are embedded in the design doc.

What was found: re-ran `estimate-audit -plan-only` (TPC-H, `:65433`/`:65432`)
and `capture-tpcds.sh` (TPC-DS SF0.25, `:65437`/`:65438`) against the live
bench clusters (none restarted), then diffed with `pg-plan-parity-diff.py`.
Corpus-level category counts are essentially unchanged since the 2026-09-14
M0142 baseline (TPC-H join-order 14->14 exact; TPC-DS join-order 89->90,
within one query; both non-regression floors held: TPC-H match=6, TPC-DS
match=2). **That aggregate hides per-query movement**, so the two specific
queries the four blocked pricing rounds (R70/R89/R98/R99) actually converged
on — Q9 (R53/R68) and Q96 (R96) — were re-examined individually, per K50's
warning that corpus roll-ups hide per-query signal:
- **Q9/B2: the blockage MOVED.** Confirmed M0138-0006's estimate finding
  byte-for-byte (goopg 146 rows, PG 60,125, actual 175 — estimate did NOT
  move toward PG) and added a category-isolation + topology result this task
  is new: Q9 is `SHAPE-DIFF [join-order]`, a single category with no
  confound, and the join-spine trace shows both engines share the FIRST join
  (`partsupp ⋈ part`) then diverge in relset choice at **every** subsequent
  step — goopg walks `+lineitem→+supplier→+orders→+nation` (FK-indexed
  Nested Loops), PG walks `+supplier→+nation→+lineitem→+orders` (all Hash
  Joins, `orders` outermost). This is a genuine **topology** divergence, not
  "the same shape priced differently" — R53/R68's method (instrument a
  narrow cost margin between two candidates goopg's own search already
  produced side by side) has nothing to attach to here, because there is
  only one candidate topology observed. The open question is now **shape
  generation** (does goopg's DP even construct PG's topology as a candidate,
  and at what price?), which needs an instrument that does not exist yet.
- **Q96/B5: the blockage is UNCHANGED.** B5 is PG-side observability (EXPLAIN
  doesn't expose PG's internal hash-join cost inputs, and running real PG
  over a copy of goopg's own data directory crashes PG's parallel workers
  per R99) — structurally orthogonal to everything M0137-M0140 landed. Live
  re-check today: Q96 is still `SHAPE-DIFF` with `join-order` tagged, not
  newly resolved.

Key symbols: `internal/optimizer/joinsearchseam.go`/`joinsearch.go` (DP loop,
where M0142-0003a's proposed `GOOPG_JOINSEARCH_TRACE` instrument would live —
does not exist yet); `scripts/pg-plan-parity-diff.py` (category tagging —
`join-method` derives from matched-pairing comparisons, so Q9's total-
topology divergence collapses to a single `join-order` tag rather than also
tagging `join-method`); `cmd/estimate-audit/main.go` (`-plan-only`, `-serial`
default-true — the TPC-H capture tool); `scripts/capture-tpcds.sh` (TPC-DS
capture tool, GUCs pinned in-script).

Gates run: `go build ./...` clean (no `.go` file touched — pure recon, same
precedent as M0138-0001/-0005/-0006 and M0141-S2a). No `go test` run for the
same reason. No values/plan-gate sweep (no behavior changed — the two live
captures above ARE the measurement, not a regression check). Pre-commit
hook's pgbench smoke: ran and PASSED as part of `git commit` (0 failed,
~142 TPS select-only). `make ralph-state-guard`: found the same
"status=running vs stale progress=completed" pattern as last loop
(left over from a prior loop's clean exit), self-repaired to "in_progress",
re-ran and confirmed OK.

In-flight: none. All four bench clusters (`:65432`/`:65433` TPC-H,
`:65437`/`:65438` TPC-DS) were left running exactly as found (none started
or stopped this loop). `/tmp/estimate-audit-m0142-0001` binary was removed
after use.

Next step: M0142-0003a is now the concrete, selectable next M0142 task — it
is instrumentation-only (env-gated trace, no default-path behavior change)
and has a fully specified deliverable (does goopg's DP construct PG's Q9
topology as a candidate, and at what cost vs the chosen chain's 124,545?) in
this loop's design doc and in `.ralph/fix_plan.md`'s M0142-0003a line.
M0142-0003b is explicitly NOT selectable yet (depends on -0003a's finding).
Re-read the `## Current Priority` banner fresh next loop before picking:
M0137-M0140 are fully closed, M0141's two live slices (`M0141-S2a-fix`,
`M0141-S2b`) both still need their own scoping pass before being
same-loop-sized (per the last two loops' findings — do not re-attempt
either without that scoping work first), and M0142 now has one selectable
task (-0003a) alongside M0143 (gated on nothing). Do not re-run M0142-0001/
-0002 or re-derive Q9/Q96's verdicts — both are now closed facts recorded in
this loop's design doc and ledger row.
