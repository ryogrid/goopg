# TODO_ALL revision notes — agent review and disposition

`TODO_ALL.md` was drafted 2026-09-04 consolidating take2 / take3 planner /
take3 executor / minimize_datum remaining work, then reviewed by three
read-only agents in the pattern of `REVIEW.md`:

- (a) source accuracy against the four source TODO files + MD companions,
- (b) tree verifiability of every commit hash / file anchor / open-status
  claim,
- (c) adversarial design and sequencing review.

All findings below were folded into `TODO_ALL.md` (and, where the finding
pointed outside it, into take3 `TODO.md` / MD `TODO.md`) before commit.
Reviewer verbatim is paraphrased; dispositions are exact.

## Status reconciliation (verified, no change needed)

- P0-08 (`f4a5e7e75`) and P0-13 (`82dd30bbc`) are DONE in the tree; take3
  `TODO.md` listing them `[ ]` is stale. Disposition: closed in place
  (M3 below), `TODO_ALL.md` records "stale, do not redo".
- EX0–EX3 landed/dropped/blocked states all confirmed against commit
  hashes; MD blocker (3) (missing instruments) is discharged.
- P0-05/06/07 still open confirmed (no `plans-pg/` fixtures).
- P2-02d/e/f, EX4-01, EX5-01, EX3-04, EX3-06 confirmed still open.
- P1-14b: five estimator slices landed in take2; only the remainder
  (regex, multibyte case-folding, small-hist prefix-range) is open.

## Corrections applied to TODO_ALL.md

1. **P4-01 batch arithmetic.** The draft cited `Batches:` 8→4 / 128→64.
   The P4-01 DESIGN retake on HEAD records witness Batches:2 with live
   flip 2→1 (widths 1096→776/896→640/896→736/710→582); the 8-batch figure
   is the pre-retake baseline. §0 row and D-05 corrected to the measured
   pair.
2. **P4-01 deferred slices (a)/(b) were ownerless.** DESIGN.md defers (a)
   merge/NL input policy, (b) scan-node application, (c) upper targets;
   the draft owned only (c), and the EX1-04 merged half blocked on (a)
   had no checkbox. Split into B-01a/b/c; B-01a unblocks the merged half.
3. **C-19 dropped P5-03.** Split C-19 into C-19a–h covering P5-01…P5-08
   one commit each, restoring the plain-IndexScan `drivingScan`
   extension.
4. **A-01 under-covered take2 P0-04's remainder.** Folded in the
   `IndexOnlyScan` missing-`Alias` stamp and the rtable-order numbering
   (implement or ledger-file).
5. **§1 graph gaps.** Added F-01→D-05, F-02→D-05, EX1-exit→E-02 edges and
   the E-15 spike edge; added the P2-08/P2-10 resume-condition note.
6. **Stale line anchor.** D-06 cited the sort-comparator warning at
   `operators.go:898-900`; it moved to `:1010-1015`. Corrected (the same
   staleness exists in `02-*.md` and is noted, not fixed, here).
7. **D-05 blocker halves (M1).** For hash-join geometry the load-bearing
   EX1 dependency is the build half, not the sort half. Added E-14 (EX1
   build-half redesign, no second truncation) and re-gated D-05 on A-06 +
   E-14 + A-05; marked E-01/E-02/E-12 `[!]` on their true EX1/B-16
   predecessors instead of prose-only sequencing.
8. **A-05/D-01 circularity (M2).** Resolved single-owner in favour of
   Track A (A-05); D-01 carries a pointer.
9. **SKIP criterion 2 (M4).** Scoped to non-acceptance items and gated on
   take3-owner sign-off or a second independent measurement, consistent
   with `.ralph/AGENT.md` completion-and-deferral discipline.
10. **Mega-items vs one-checkbox≈one-commit (M5).** Split C-19 (→a–h),
    C-20 (→a–h), B-12 (→d/e/f), B-17 (→a–e); marked B-05 and D-08
    `[EPIC — split before start]` and added ground rule 11 binding the
    plan to the split rule.
11. **P2-04 cache-key half (M6).** New B-18 owns it plus the four
    remaining P2-02 GUC-effect fixtures.
12. **D-05 gate restatement (M7).** The gate no longer points bare at
    `06 §3 MD-04` (which still names Q9); it restates the
    operator-plus-shape-class gate inline, adds the fifth (composite
    multi-key) lane, and reports against the measured pre-state.
13. **F-items ownerless (M8).** §7 converted to F-01/F-02/F-03 checkboxes
    with §1 ordering edges.
14. **C-14/E-03 circular wait (M9).** New unconditional E-15 owns the
    13 §8.7 contract-publication spike; E-03 stays conditional on C-14.
15. **EX3-03 step-2 resume (B2).** New E-16 owns the blocked,
    implemented, unit-green threading with its resume patch and
    spill-cost-calibration precondition.
16. **D-10 vs SKIP ambiguity.** A SKIPped site unblocks D-10 only with an
    explicit out-of-scope ledger row.
17. **D-11.** Now also files the ownerless TOAST-pointer ledger row and
    any deferred D-08 keep-open rows.

## Licence wording (B1) — deliberated, not merely applied

Reviewer (c) held that A-06's "geometry-free slices proceed without
prejudice" downgrades REVIEW.md's "do not start MD-01" into a silent
authorisation. The authorised approach (proceed with unblocked tracks)
is preserved but restated so it cannot read as permission: **no MD
commit lands on `master` until written take3-owner acceptance is
recorded in A-06**. Until then D-02 (document-only) may complete, D-04
(throwaway, deleted code) may measure, and D-01/D-03/D-09 may proceed
through reviewed design and flag-gated prototype only. Landing code
without acceptance re-opens B1 — stated in the item, not in prose.

## Changes outside TODO_ALL.md (same commit)

- Take3 `TODO.md`: P0-08 → `[x]` (`f4a5e7e75`), P0-13 → `[x]`
  (`82dd30bbc`), P1-14b → `[~]` with landed slices recorded and remainder
  scoped. Rationale: a plan declaring take3 `TODO.md` authoritative for
  sequencing must not leave it contradicting the tree.
- MD `TODO.md`: MD-04 gate witness and progress-log pre-state aligned to
  the measured pair (modelled 128/64 retained as superseded history).

## Dropped-table hygiene (verified, untouched)

EX1-03, EX2-02b, EX3-05-cutB, P1-06, P1-10 (take2 decline honoured via
B-10's verify-consumer-first guard), P6-03/04/05 (recorded
must-not-delete inside C-20a–h scope) are nowhere scheduled as work;
all drop ledgers verified present.

(End of file)
