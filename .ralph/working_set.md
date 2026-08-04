Task: M0127-P5.6-g-iv — Q19, the only estimator defect TPC-H could prove. DONE,
documented, committed. Nothing in flight.

Files: internal/planner/qual_canonical.go (NEW, `canonicalizeQual` =
PG `find_duplicate_ors`/`process_duplicate_ors`, prepqual.c), qual_canonical_test.go
(NEW, 9 tests), exprkey.go (`strictParserExprKey` + `structuralKeyWriter.strict`),
planner.go `planSelect` (one call site, parse tree NOT mutated).

Four things the next loop should NOT re-derive:

1. **The defect was a missing preprocessing pass, not a selectivity bug.** Q19's
   join clause `p_partkey = l_partkey` is repeated in all three OR arms;
   uncanonicalised it is priced twice (equi-key + DEFAULT_EQ_SEL per arm) and
   the three single-relation conjuncts common to all arms are priced NOWHERE.
   est 1 → 309 vs actual 131; parity excess 126.5× → 2.3×; ratchet
   `parity_violations=0 shape_mismatches=0`.
2. **TPC-H can only reach Q12 and Q19** — they are the only two texts with an
   OR in the WHERE (Q15's `or` is `CREATE OR REPLACE VIEW`). Q12 is the
   no-winners control and came back bit-identical. Do NOT read the 2-query
   audit as a full sweep; the other 19 were not re-run.
3. **`strictParserExprKey` exists for a reason** — `parserExprKey` drops table
   qualifiers (M0097-0003), so `a.x = 1` and `b.x = 1` compare equal under it
   and hoisting one would silently lose rows. Pinned by a test.
4. **M0058-0004's `commonEquijoinsAcrossOr` (joinorder.go) is now redundant in
   principle** — it computes the same intersection for the join EDGE only. Left
   in place deliberately (it runs before `planSelect`'s canonicalisation);
   removing it is a separate, measurable change.

Evidence: `analysis/leftdeep-joins/2026-08-05-p56giv{.txt,.plans.txt,-README.md}`
plus the Q19-only `-q19` pair. Docs: 09 §5.9, IMPLEMENTATION-TODO P5.6-g-iv,
design README index. 1 ledger row (3 deferrals: constant handling,
UPDATE/DELETE quals, `extract_restriction_or_clauses`).

Next step: per the banner, the next M0127 item. **M0127-P5.6-g-i (the carried
DS05 sweep) still goes first and now matters MORE** — it must attribute to BOTH
P5.6-g and this pass, and TPC-DS is where a qual-canonicalisation change's
plan-shape blast radius is actually measured. Blocked again this loop: the
nightly CI batch ran the whole loop (testport stage wedged for ~2 h, then
pgbench) and `scripts/tpcds-sf05-regression.sh` self-refuses while it holds the
host. Then P5.6-g-ii (HashAggregate arm + Q18 dedup shape).

Gates run: build + vet; planner `go test` PASS (9 new); UNITS PASS
(`/tmp/units_p56giv.log`, 38 pkgs); **tpch-spotcheck PASS** (Q12 rows=2,
Q13 rows=35 — `/tmp/spot_p56giv.log`); audit with the parity column on Q12+Q19;
pgbench smoke via the commit hook. No DS05 (see above).

Nightly triage: the same 17 `AI-20260804-005028-*` subjects, all already filed
under M-NIGHTLY. Nothing new to file.

In-flight: none.
