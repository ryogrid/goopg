Task: M0142-0015 — recon: does M0142-0012 flip Q45's `item`/`customer_address`
DP-search tie because its formula is wrong, or because of the same near-tie
class M0142-0003b/0003c found for Q9? **DONE and committed** this loop.
Measurement-only, no production code changed.

Files: `.ralph/fix_plan.md` (M0142-0015 `[x]`, M0142-0003c line annotated
with the Q45 cross-reference). `docs/design/0100-0149/m0142-0015-q45-tie-break-is-near-exact-in-both-engines.md`
(new). `docs/design/README.md` (indexed). `analysis/m0142/m0142-0015-q45-{goopg-explain,dptrace,pg-default-and-forced}.txt`
(new, committed raw evidence).

Key symbols: none touched (recon-only). Trace instrument used:
`GOOPG_PGSHAPED_DP_TRACE=1` (`internal/optimizer/joinsearchtrace.go`,
`pathtrace.go`'s `DPPATH` lines) — already existed, same as M0142-0003a.

Findings this loop: goopg's own level-5 DP search for Q45 has TWO
`nestloop.index` candidates both `verdict=accepted` (neither dominated)
differing by `2e-12` (`9674.299956003799` ca-first/PG-order vs
`9674.299956003797` item-first/goopg's actual choice) — `setCheapest` just
takes the literal float minimum; the PG-matching candidate is registered
FIRST (`created=1`) but still loses, so this is NOT the Q9-style exact-tie/
registration-order mechanism (M0142-0003b), it's a genuine (if
imperceptibly small) numeric difference. The feeding level-4 legs are NOT
tied (9606.17 vs 9596.43, real 9.74 gap) — level-5's marginal costs
compensate almost exactly. Forced real PG 18.3 (`join_collapse_limit=1`,
explicit left-deep JOIN) into the alternative order and got an IDENTICAL
displayed top-level total (9827.36 both ways) despite a real 5.29 gap in
the child Nested Loop node — same qualitative near-tie-by-cancellation
shape in PG's own planner, at whatever precision its 2-decimal EXPLAIN
output can show. Verdict: same class as M0142-0003c's still-open question,
not a new M0142-0012 defect; no code change follows. Annotated M0142-0003c
to use Q45 as a second corroborating witness (not a Q9-only question) when
it's next picked up — did not file a brand-new task since M0142-0003c
already covers exactly this question.

Next step: pick the next task per `.ralph/fix_plan.md`'s `## Current
Priority` banner (still M0137–M0143, item 4: M0141/M0142 remaining
slices). Open M0142 items as of this loop: **M0142-0003c** (now a 2-witness
recon — Q9 + Q45 — level-6/level-5 enumeration-order-vs-cost-tie
parity vs PG's `join_search_one_level`), **M0142-0005** (Memoize/
probe-multiplier interlock, B6+B8, sized like an executor slice),
**M0142-0008** (recon: how much of goopg's plan shape is chosen by forced
shapes vs real costing — not yet read this loop, check its scope before
picking), **M0142-0016c** (does PG qerr-match the M0142-0016b Q33/Q54/Q56
residual-selectivity findings if forced into an analogous plan). None is
mandated over the others by the banner; M0142-0003c is a reasonable next
pick since it now has two corpus witnesses and was explicitly flagged as
the natural continuation by both this loop and the M0142-0012/0014/0016
chain, but M0142-0005/0008 are equally banner-eligible.

Gates run: no production code touched, so the practice-card gate suite was
not required this loop (git diff -- internal/ empty, confirmed before
finishing). `make ralph-state-guard`: found the same pre-existing stale
progress-marker inconsistency as the last several loops (status=running vs
a stale progress=completed marker from a prior loop's clean exit),
self-repaired to in_progress, then passed clean.

In-flight: none. Nightly CI batch (run_id `20260916-035206`, started
03:52) was running concurrently the whole loop — units/race/testport all
already FAILed by the time this loop started (pre-existing, not caused by
this loop; matches the already-filed M-NIGHTLY items from the
20260914-235643 run, action-items.md for THIS run not yet generated since
the batch hadn't reached its summary stage). Left it running untouched;
this loop's own private throwaway servers (goopg :5534 on a
`/tmp/pp-m0142-0015` SF0.25 data clone, PG reference `:65438` bracketed
`start pg`/`stop pg`) were both stopped and reaped before finishing (`ps
aux` and `bench/tpcds/server.sh status` both confirmed clear); `/tmp/pp-m0142-0015`
and `/tmp/goopg-m0142-0015` scratch dirs removed. Did not re-check whether
the new nightly run (once it completes) needs a fresh M-NIGHTLY triage
pass — that's the very next loop's job per the PROMPT.md nightly-triage
step, since action-items.md still reflects the OLDER 20260914-235643 run
as of this loop's end.
