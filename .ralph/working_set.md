(idle — nothing in flight)

# Loop #79 result — M0145-0019 recon DONE; its hypothesis is REFUTED

Banner: OWNER GO sequence M0145-0009 [x] → **M0145-0019** (this loop) →
M0145-0020 next. Recon: no `internal/`/`cmd/` file touched (C1).
Design: `docs/design/0100-0149/m0145-0019-nl-costing-derived-inner.md`.

## The finding — the NL is NOT mispriced
Reproduced Q78's election at SF1 (private `:5561` clone, knob arm,
`GOOPG_DERIVED_FIREWALL=off`, EXPLAIN-only). `GOOPG_PGSHAPED_DP_TRACE=1` at
the final joinrel shows BOTH candidates generated AND accepted:
- `join.hash`     startup=16457.31 total=**37659.80**   accepted
- `join.nestloop` startup=5494.86  total=**1147507.76** accepted
`add_path` keeping both is CORRECT — the NL has the lower startup. 0018's
"losing to nothing" reading does not hold.

## The real divergence — the fraction is applied to the WRONG REL
`searchCtx.finalPath` (`internal/optimizer/joinsearch.go:317`) calls
`getCheapestFractionalPath` at the JOIN SEARCH ROOT. f = 100/10317 = 0.009693:
hash **16662.82** vs nestloop **16564.09** — NL wins by 0.6%, is then fed to a
`Sort` that consumes every row, and 0.6% becomes 30x. That is the SF1
regression 0018 caught.
PG can't do this: `planner.c:439` runs `get_cheapest_fractional_path` on
`final_rel` (top UPPER rel, Sort already priced); `planner.c:5314`
`create_ordered_paths` takes `cheapest_total_path`; `planner.c:7646`+
`make_ordered_path` returns NULL for any unsorted non-cheapest-total input.
`finalPath`'s doc comment cites `planner.c:437` — **citation right, rel wrong**.

## Both of M0145-0018's remaining options are now wrong targets (ledgered)
(c) has nothing to fix — changing NL pricing to move the election is tuning
toward a plan (R6). (b) suppresses a symptom whose rule needs NO derived input:
any `ORDER BY … LIMIT` over a join with a low-startup/high-total path elects
the same way. 0018 stays `[!]`; the owner re-takes it after the fix.

## Filed: M0145-0019a (the faithful third option)
Move `getCheapestFractionalPath` to the top of the upper-rel lattice; feed the
ordered step from cheapest-TOTAL. Expected movement + measurement stated (S5).
**Blast radius: NOT a Q78 fix** — live since M0127-P5.9 (2026-08-06); every
ORDER BY+LIMIT join query can move. Full value gates + own parity capture.

## Gates
Recon, zero production diff → no value gates (C1). state guard OK (auto-repair
of the stale completed marker); pgbench smoke via the commit hook.
Lane stopped, `/tmp/g78` removed, port 5561 free. Evidence kept:
`tmp/m0145-0019-q78-{firewall-off-plan,dppath-final-joinrel}.txt`.

## Next loop
**M0145-0019a** (the fix this recon names) or M0145-0020 per the banner order —
the banner lists 0019 then 0020, and 0019a is 0019's own deliverable, so take
0019a first unless the owner reorders.

## Owner escalations — two open + one to re-take
partition_aggregate's inventory row; template1 namespace collision (A vs B).
M0145-0018's option choice needs re-taking on this loop's evidence.
