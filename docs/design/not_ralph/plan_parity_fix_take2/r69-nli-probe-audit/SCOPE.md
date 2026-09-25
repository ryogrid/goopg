# R69 SCOPE — NLI probe-term attribution + fix iff mis-transcribed (2026-09-11)

Follows R68 STEP-0 (`r68-joinorder-costing-step0/STEP0.md`, committed
`1959a4c`): Q9 diverges at L6 — NLI-winner 117342.97
(`outer={0,1,2,3,5} inner={4}`, `inputtotal=96778.63`) vs
PG-partition-hash 528–539k dominated. Sizing/admission/
parameterisation OUT (re-run, not carried). This slice attributes
goopg's join-added (117342.97 − 96778.63)/303093 ≈ **0.068/probe**
term by term against PG's price for the same probe, and fixes iff a
term mis-transcribes PG.

## 0. Baseline

- STEP-0 numbers (`/tmp/pp2/r68/`, re-verified at review): L6 NLI
  accepted, both hash orientations dominated (528266.23 with
  336940.93 over startup; 539118.03 with 373109.58 over), merge
  439k–991k, plain-NL billions. PG live (`pg-q9.txt`, serial, 64MB):
  top Hash Join 146504.58..147406.46, i.e. PG prices the same join
  ≈98k above its inputs. `SHOW work_mem` 64MB both engines
  (`show-*.txt`).
- Composition sites (verified, not assumed): the NLI price is
  `nestLoopInnerRescanCost` + `nestloopCost` (`joinpathsnli.go:334-337`;
  there is no `joinsearchnlicost.go`) + residual
  `qualEvalCost(len, o.Rows*in.Rows)` (`:337`), with the
  `initial_cost_nestloop` enable term at `:355-363`. `nestloopCost`
  reproduces `final_cost_nestloop` (doc `cost_funcs.go:692`, body `:723`,
  P2-07 ntuples fix landed). `nestLoopInnerRescanCost` (`joinpathsmemoize.go:460`):
  Memoize inner → own rescan; parameterised inner → `pathRescanCost`
  (full re-execution per outer row — PG's `cost_rescan` default arm);
  else materialised-build/rescan + spill arm.
- What is NOT yet decomposed: the 0.068/probe join-added figure into
  (rescan-run, cpuTuple×ntuples share, residual-qual share) with
  PG-analogous numbers beside each. That decomposition IS this slice's
  phase 1. (A draft quoted 0.365/probe — that amortizes the outer
  input run cost into the probe; corrected in STEP-0 review.)

## 1. The cut (two phases, one round)

**Phase 1 — attribute (no behaviour change).** Temp per-term logging
(reverted before commit, same discipline as R53-Slice-1) on the L6 NLI
offer: outer run/startup, inner rescan-run AND rescan-startup
(`nestLoopInnerRescanCost` arms — note the arms return only
`(build, run)`, so the dropped rescan-startup is invisible to them;
log the call-site `0` explicitly), `cpuTupleCost×ntuples`,
`qualEvalCost` residual share (run; startup folded into Total —
record the folding), enable term; beside each, PG's term from
`costsize.c` (`initial_cost_nestloop` ~:3282,
`final_cost_nestloop` ~:3349, `cost_rescan`, `cost_qual_eval`) and the
index-probe input price (`btreeIndexAMCostPages` num_scans arm, R59 —
loopCount source and ML-cap behaviour re-verified, not carried).
Known non-owners, recorded not audited: SEMI/ANTI early-stop, qual
STARTUP, tlist-per-output-row. Gate: temp-instrumented binary plans
byte-identical to clean HEAD on the Step-0 corpus (Q9 + Q6/Q13/Q22) —
the instrument is proven inert before anything is concluded from it.

**Phase 2 — fix iff mis-transcribed.** Each goopg term either matches
its PG counterpart (recorded, kept) or does not (fixed with the PG
formula + a unit pin per term, pre/post numbers). If every term
matches (NLI faithful): findings-only close — the winner stands on
its own price and Q9's flip belongs to the width program (slice (b)
ledgered, K65; R53-Slice-1's no-spill 44–77k stays STALE until
re-measured there, never carried here).

## 2. Blast-radius honesty (read before implementing)

If the probe is underpriced by a large factor, fixing it reprices
EVERY NLI winner corpus-wide (NLI took L3–L6 post-R59/R64; TPC-H has
36 NL lines). This round therefore does NOT promise ZERO moves or a
Q9 MATCH (K22 — and arithmetically it cannot: flipping to hash needs
a raise past ~410923 (528266.23−117342.97), i.e. ~20× on the 20.5k
join-added; a lower-direction fix stays NLI too). What it promises
instead, per R10 DESIGN §7 (explained, not outweighed): every moved
query adjudicated toward PG's plan for the same shape (PG's NL sites:
Q7's lower chain etc. — adjudicate, don't assume), values green both
corpora, and NO away move lands unexplained. This SUPERSEDES STEP0
§3's "ZERO EXTRA flips" bar for the implementation round (stated
here, not silently diverged). Awaiting PG's own NL price for the Q9
shape (which PG never emits) is not required — the audit is
term-by-term against `costsize.c`, not outcome-against-oracle.

## 3. Falsifiable predictions

| # | Claim | Verdict on miss |
|---|---|---|
| P0 | attribution table KEYED OFF PG's term list (not goopg's): for
each of rescan-startup (`(outer−1)·rescan_start`, costsize.c:3301-3302
— all three call sites pass literal `0` as `innerRescanStartup`
today), rescan-run, cpuTuple×ntuples, qual (run AND startup,
`:3507`), enable term, tlist-per-output-row (`:3512-3513`), SEMI/ANTI
early-stop (`:3388+`): the goopg counterpart or a recorded omission
with a number. Either direction of mismatch (goopg term with no PG
counterpart, or PG term goopg omits) trips STOP — the audit is
incomplete until the table is total in both directions |
| P1 | fix lands iff a term mis-transcribes; faithful → findings-only close (no code) | code change without a named mis-transcribed term, or no-change despite one → STOP |
| P2 | values 24/24 MATCH pre/post both corpora gates (digest + SF0.25 sweep all-zero) | ANY values move → STOP (cost work must not change answers) |
| P3 | every plan move adjudicated toward PG's same-shape plan; zero unexplained away-moves | an away-move without a named mechanism → STOP (R10-§7) |
| P4 | L6 re-measured after any fix, margin direction recorded; NO Q9-MATCH prediction (K22 — conjunction with width owns the flip) | claiming the flip from this slice alone → STOP |

## 4. Sibling audit

- `nestloopCost` shared with the plain-NL arm (`pathgen.go`): a fix
  inside it moves BOTH arms — corpus A/B must cover plain-NL shapes
  (Q4/Q22 carry NLs) or the fix lands in the NLI-only path
  (`nestLoopInnerRescanCost` / probe input pricing). State the landing
  site explicitly; grep-oversize the scope first, compiler-second.
- R59's num_scans arm and `loopCountFor`/`get_loop_count` parity: inputs
  to the audit, re-verified (R59's Q7 numbers are stale context, not
  evidence here).
- `qualEvalCost` shared with other arms: same land-or-scope rule.
- Trace channels untouched (no new instrument kinds beyond the temp
  per-term print, reverted).

## 5. Gates (implementation, all FOREGROUND)

1. `go test ./internal/executor/ ./internal/optimizer/
   ./internal/testutil/estimateaudit/` green (new per-term pins
   pre/post; no `-count=1`); `go vet` clean.
2. `scripts/tpch-spotcheck.sh` PASS (re-run iff `:65433` free, else
   the standing deferral rationale carries — cost round, values gates
   bind regardless).
3. Values A/B 24/24 (digest) + DS SF0.25 sweep foreground, private
   lane (P2).
4. Explain A/B + pp vs fixtures AND live PG (P3–P4; pre-flight the
   NLI-winner census so the move set is bounded before judging it).
5. `make plan-gate` triage (expect join-method verdict moves on a real
   fix; re-pin or R65-precedent opt-out with pre/post-identical
   rationale — a fix that moves nothing needs its no-move explained,
   not celebrated).
6. `REPORT.md`, review, commit (explicit pathspec, `-n` per the
   goal's binding round process — the hook's pgbench smoke is covered
   by this round's own heavier gates: values A/B, sweep, spotcheck
   rule) + push.

## 6. Ledger (carried)

Slice (b) width/footprint (conditional follow-up); R51 items 2–3; R52
§4.2; R54 follow-ups; #6/R61-#4/(b) watches; NLI staleness comment;
K58 (`scaleByFloat` truncation) + K59 (threshold crossings) as
adjacent suspects if the attribution lands near them — suspects, not
owners, until measured.
