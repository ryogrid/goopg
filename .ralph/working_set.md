M0127-P6.4 COMPLETE — supersession stamps + ledger rows (LAST M0127 task)

M0127 is now fully closed. All P6.1–P6.4 tasks are [x].

Task: M0127-P6.4 — supersession stamps, README index flips, ledger rows
Files:
  - 23 design docs stamped with `superseded by: leftdeep-joins/` headers
    (0034-0001, 0038-0001, 0043-0002/0003, 0063-0002, 0125-0002/0013/0046,
    all 13 0126-*)
  - docs/design/README.md: 19 row status flips (0034, 0038, 0043-0003,
    0125-0002/0013/0046, all 13 0126-* from draft→superseded)
  - docs/design/cost-model/09-verification-and-acceptance.md: §3 MHJ
    allow-list item struck, (MHJ) reference removed
  - docs/design/leftdeep-joins/08-migration-and-removal.md: §5 COMPLETED
    note added
  - docs/design/leftdeep-joins/IMPLEMENTATION-TODO.md: P6.4 checked off
  - .ralph/deferral_ledger.md: 5 new rows (GEQO, skew buckets,
    SpecialJoinInfo semi/anti in-DP, shared spilling builds, full
    join_order_restriction inference — last two carry
    join_is_legal-inference-dependent marker per 08 §5)
  - .ralph/fix_plan.md: M0127-P6.4 checked [x]; M-NIGHTLY
    AI-20260807-004620-001 appended to existing regress/truncate task

Key symbols: N/A (doc-only change)
Hypothesis/Findings: M0127 closed — all 4 P6 tasks done. The PG-shaped
  DP is the ONLY join-order search; MHJ is fully deleted; the
  supersession trail is complete.

Next step: M0128-P1.1 (special-join inference / join_is_legal, per the
  Current Priority banner — M0128 is unblocked now that M0127-P6.4
  filed the join_is_legal-inference-dependent ledger rows P1.2/P1.5 need).

Gates run: `make ralph-state-guard` OK. Build `go build ./...` OK.
  No code changed — doc-only, no test/sweep gates needed.

In-flight: none
