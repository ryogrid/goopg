Task: M0125-0031 FIRST MOTION — the warm TPC-H power sweep at HEAD. DONE and
committed this loop (#11, 2026-07-30). The umbrella item M0125-0031 stays OPEN.

Files:
- `analysis/m0125-0031-warm-tpch-20260730.md` — NEW report (4-arm table, invariant,
  noise band, Q21, (b) target list) + `.../w{1,2}-*/` raw TSVs
- `scripts/tpch-relsize-arm.sh` — arm flags now EXPLICIT (`0`/`2`); after the
  M0125-0005 default flip an unset var would have turned c1→c2 and w1→w2
- `docs/design/0125-0028-warm-stats-programme.md` — §-0031a execution record
- `analysis/tpch-relsize-fallback-20260730.md` — noise-band superseded note
- `.ralph/{fix_plan,deferral_ledger}.md` — -0031 body + M0125-0032 filed; row 594
  flipped `resolved`, one new row for Q21

## Result (all four §D5.1 arms, quiet host, 1 binary / 44 executions)

c1 693.8 → c2 494.0 → **w1 413.3 → w2 420.1 s** (21 comparable queries).
Warm vs S-cold at the shipped default: **−15.0 % (1.18× faster)**, rows identical.
Round 4's five regressions do NOT reproduce; **Q8 (its 53× loss) is the biggest
win, 8.5×**. Q5's 22.8× win is gone (M0077 fixed Q5 already).

Three findings later loops must honour:
1. §D3's invariant MEASURED — warm + `GOOPG_RELSIZE_FALLBACK=0` vs
   `warm-stats-base` = 22/22 MATCH `structural` AND `strict-text` → relsize is an
   S-cold-only safety net (ledger 594 resolved; W-arms discharged).
2. Harness noise band ≈ **±17 %** per query, not ±4 % (identical plans moved
   1.17×) → no sub-1.2× single-run ratio on a sub-20 s query is evidence.
3. **Q21 times out in ALL FOUR arms** (612/672/381/384 s, 14.4 GB RSS) → shape
   class, filed `M0125-0032`.

## NEXT (banner order)

**Still owed by -0031: goal (a) entirely** — run ONE full **warm SF0.5 gate**
(quiet host, ~1 h, four `QUERIES=` chunks on one binary) and read the warm
timeout class against the **13** baseline. If the host is BUSY, take
**`M0125-0026`** instead (host-independent plan capture; it now gains the free
warm arm). Then -0031(b)'s TPC-H fixes, scoped to the seven queries that are
69 % of the stream (Q5 Q9 Q4 Q18 Q15 Q7 Q17) + Q21 via -0032.

Gates run: 4-arm timed sweep (44 executions, engine-id stable start+end);
plan-diff vs `warm-stats-base` 22/22 MATCH in both modes; `make
ralph-state-guard` OK; pgbench smoke via the commit hook. No Go code changed
this loop (harness/docs only), so no unit gate.

In-flight: none.
