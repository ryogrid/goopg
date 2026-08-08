Task: M0129-S9.4 DONE — RIGHT→LEFT flip + FULL→RIGHT partial reduction

Files:
- internal/planner/reduce_outer_joins.go: pre-loop RIGHT→LEFT flip (first join),
  pre-loop FULL→RIGHT→LEFT flip (only right constrained), FULL demotion fix
  (leftConstrained→LEFT matching PG prepjointree.c:3325-3330). After flip,
  LEFT→INNER and LEFT→ANTI checks apply normally.
- internal/planner/reduce_outer_joins_test.go: 3 tests updated (RIGHT demotion
  asserts structural swap, RIGHT no-demotion expects LEFT after flip,
  FULL one-side asserts structural swap for FULL→RIGHT→LEFT, FULL resets test
  updated for pg-matching demotion). 7 new tests: RIGHT flip basic, RIGHT flip
  then ANTI, RIGHT flip INNER wins over ANTI, RIGHT multi-join chain,
  FULL→RIGHT→LEFT flip, FULL→LEFT only-left-constrained, FULL both-sides
  structural. Total 39 tests (23 existing + 9 S9.3 + 7 S9.4).

Key symbols:
- applyDemotion pre-loop: RIGHT→LEFT and FULL→RIGHT→LEFT flip (first join only)
- applyDemotion FULL case: leftConstrained→LEFT (FIX: was rightConstrained→LEFT)
- applyDemotion RIGHT case: RIGHT→INNER for non-first positions (not flipped)

Hypothesis/Findings:
- PG's FULL demotion: left nonnullable→LEFT, right nonnullable→RIGHT→flip.
  goopg had these SWAPPED (right→LEFT), producing wrong-direction LEFT join
  for FULL with only one side constrained. Fixed to match PG.
- PG's nonnullable propagation is TOP-DOWN (parent→child); goopg's flat chain
  is LEFT-TO-RIGHT. This makes goopg more aggressive for INNER-ON→FULL chains
  (INNER→ON nonnullable findings reach the FULL join; PG's don't), but the
  demotion is always semantics-preserving (never loses rows).
- First-position flip covers all practical cases: pg_dump and benchmark queries
  use only first-position RIGHT/FULL or none at all.
- Deeper RIGHT/FULL→RIGHT flips need a nested-join AST (JoinExpr.Left/Right
  as Node instead of RangeVar). Ledgered at deferral_ledger.md row 2026-08-08
  M0129-S9.4.
- DS05 showed zero plan movement (99/99 same plan shapes, verdicts, runtimes).

Next step: M0129-S10 — ExecError.Pos → wire FieldPosition ('P' error field).
  Last task in the M0129 implementation order before S4/S5.

Gates run:
- go build ./...: PASS
- All 39 reduce_outer_joins tests: PASS
- Full planner suite: PASS (1.145s)
- Pre-commit (units): PASS
- tpch-spotcheck: PASS (Q12=2 Q13=35, 26s)
- DS05 delta: PASS (99/99, zero row/checksum/plan deltas, zero runtime moves)
- pgbench smoke: PASS (413K txns, 0 failed; first run had 1 transient abort)

In-flight: none
