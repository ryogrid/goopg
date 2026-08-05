(idle — nothing in flight)

Last loop: **M0127-P5.9 RUN 4 EXECUTED** (HEAD `9e0cfe67`). Do NOT re-run it —
the evidence is committed. Result: **HOLD, not a no-go.**

1. **Five of six clauses PASS, and nothing in the evidence is attributed to
   `GOOPG_PGSHAPED_DP`.** Clause 1 PASS (23 MATCH; Q5's digests byte-identical
   to runs 2/3 ⇒ §3.4's PG adjudication carries). Clause 2 **PASS 0.982×**
   (ON 355.14 s vs OFF 361.59 s, allowance 433.91) — runs 1-3 read 1.36×.
   Clause 3 PASS, worst real query 1.36× (Q2); Q9 15.83 s vs ≤ 170.9 s.
   Clause 4 PASS — DS05 under the flag `PASS=95 MISMATCH=0 CKMISMATCH=0
   ERROR=0 TIMEOUT=0 SKIP=4`, cell-for-cell the OFF baseline. Clause 5 PASS
   (4th consecutive). §4 ratchet `parity_violations` **6 → 0** on the ON arm;
   §5 one violation per arm (the same pre-existing Q18 semi-join).
2. **The flip is held by clause 6 ALONE, and clause 6 has no instrument.**
   09 §4 specifies "verified through the §4 parity gate's spine diff";
   `cmd/estimate-audit` has ZERO occurrences of "bushy" or "spine". Its
   `SHAPE (…-only joinrel)` labels compare RELSETS — clause 6 asks about
   PAIRINGS. Runs 1-4 all scored it from a proxy (the divergence count).
3. Measured directly this loop: **PG 18.3 goes bushy on exactly Q7, Q8, Q20
   of the TPC-H 22; goopg on NONE, in either arm.** PG's Q7 partition is
   `{customer+lineitem+n2+orders} ⋈ {n1+supplier}` vs goopg's
   `{lineitem+n1+n2+orders+supplier} ⋈ customer`. Not a failure by itself —
   the clause hard-fails only on a shape the search cannot EXPRESS, and
   phase 2 (`joinsearchlevel.go:171-222`) is `joinrels.c:141-198` term for
   term with 3 unit tests — **but those tests use a synthetic 4-rel chain,
   so "enumerated and lost on cost" vs "never enumerated" is undetermined.**

Files: docs only. `docs/design/leftdeep-joins/09-…md` (new §3.10), bundle
README status line, `docs/design/README.md` index, IMPLEMENTATION-TODO
(P5.9-l), fix_plan (run-4 note + P5.9-l), 3 ledger rows. Evidence:
`analysis/leftdeep-joins/2026-08-05-p59run4-{s5-acceptance,off,on,diff,
explain-off,explain-on,audit-off,audit-on,ds05-on}.txt` (+ .plans.txt).

Gates run: the bar itself (both value arms, both EXPLAIN arms, both §4/§5
audit arms, the full DS05 sweep under the flag), pgbench smoke via the commit
hook, `make ralph-state-guard` (repaired a stale progress marker). No code
changed, so no unit-suite run was warranted.

Next step: **M0127-P5.9-l** — build the pairing/spine channel (search-level
provenance of the joinrel pairings the DP built + a comparator against PG's
chosen partition, wired into `scripts/tpch-estimate-audit-arm.sh`), adjudicate
Q7/Q8/Q20, then P5.9 re-runs clause 6 ALONE and flips or attributes.

In-flight: none.
