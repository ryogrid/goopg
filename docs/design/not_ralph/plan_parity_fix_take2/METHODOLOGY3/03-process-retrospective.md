# 03 — Process retrospective

*What is wrong with how this work is being done, with the measured cost of each
pathology. This is not a criticism of rigour — the programme's rigour is unusually
high, its self-correction is honest, and its review process catches real and
sometimes fatal errors. The problem is that the rigour is spent at the wrong
granularity, on instruments that have decayed, against a metric that is known to
be blind.*

---

# Part A — Summary

## The eight pathologies

| # | pathology | measured cost |
|---|---|---|
| **P1** | **The round is mis-sized for its most common job** — eliminating a hypothesis. | 243 of 332 commits touch no code; ~12 of R100–R130 ended in BLOCKED/UNOBSERVABLE with no code; the two best late results were *recons run outside the cadence*. |
| **P2** | **Knowledge retrieval fails, and exhortation has not fixed it.** | R127 withdrawn on 9 findings (3 fatal), all refuted by on-disk evidence; R130 withdrawn after 3 revisions, one proposing a cut landed 643 commits earlier. |
| **P3** | **The success metric is structurally blind to estimates, and match-count clauses persist despite being known-mis-specified.** | K50: an estimate change registers only insofar as it changes plan *structure* — R34 corrected a 580× cardinality error and measured exactly zero. |
| **P4** | **Instruments have decayed and produce false readings.** | K18's `$$` trap gave false structural readings in R122/R123/R124/R128 and is still live; captures are hand-stamped or unstamped; stats-epoch drift (1.31× on Q9) exceeds the effects being claimed. |
| **P5** | **Gates are routinely opted out of, and the correctness channel is under-specified.** | `plan-gate` a standing opt-out since R65; `tpch-spotcheck` deferred four rounds running — while being the only gate that caught a wrong-rows bug; **two real correctness bugs found by parity work, not by the gates**. |
| **P6** | **Findings mutate as they propagate between documents.** | "41× closer to PG" → "41× more accurate" (R126 recon → R130); "R124's increment was parity-neutral" → "the whole chain was parity-neutral" (corrected only at R128). |
| **P7** | **Debt accumulates with no retirement mechanism.** | Four default-off cost arms, one resolved-to-delete and undeleted; ~15 ledger items carried ten-plus rounds; R80 reserved and silently never created. |
| **P8** | **One error class recurs at every level despite being named repeatedly.** | "Concluded from an artefact without checking what produced it" — root-causes rev 1, R123 §6, R130 rev 1, R96 SLICE, R71. |

## The shape of the cost

- **332 commits over 7 days**, across **126 round directories** (numbering runs to
  R130 with gaps at r18, r23, r24, r80, r114 and r129). Roughly **a quarter of
  commits touch code** — by two natural definitions, 87/332 touch a `.go` file and
  104/332 touch anything outside `.md`.
- **R100–R130 (29 round directories) moved the TPC-H match count by zero** and the
  category count by two instances (R122, shipped by R128).
- Across r51–r99 (**48 directories**), **eight rounds terminated negative with no
  code**
  (R70, R78, R81, R89, R92, R93, R98, R99); R54 was an outright
  *"FAIL BACK TO DESIGN — no part of this cut lands."*
- Review **BLOCKed substantive errors in nearly every round from R120 onward**;
  R126 went rev 1 BLOCK → rev 2 BLOCK → rev 3 APPROVE, then its *implementation*
  was BLOCKed on two more. R85 needed three scope reviews, two scope amendments
  and three implementation reviews.

## What is working and must be kept

Three things should survive any reorganisation:

1. **Adversarial review.** It falsified root-causes rev 1 entirely, rejected R45
   as architecturally impossible, rejected R127 before a line was written, and
   caught R121's overstated coverage claim and R124's undisclosed shape changes.
2. **Pre-registered predictions, including negative ones**, and honest scoring
   against them. R6 scored all four predictions including the negative one; R124
   recorded `P5 FAIL` as stated; R60 recorded three letter-failures; R128
   reconciled 2 of 4 wrong TPC-DS predictions rather than quietly dropping them.
3. **Corrections struck inline rather than rewritten**, so superseded claims stay
   legible next to their replacements. This is why `01-what-we-learned.md` Part C
   could be assembled at all.

---

# Part B — Detail

## P1 — The round is mis-sized for its most common job

The binding cadence (`TODO.md:7-13`) is: *design doc → agent review → reflect →
`commit -n` + push → implement → English results report → `commit -n` + push.*
From about R46 onward it lengthened further: *SCOPE/Step-0 → review
(BLOCK / REJECT / APPROVE-WITH-NOTES) → apply notes → commit + push the scope →
measure or implement → REPORT.md → implementation review → commit + push.*

That is a reasonable shape for **building a capability**. It is badly mis-sized for
the thing rounds actually do most often, which is **eliminating a hypothesis** —
and the record shows this directly.

**Evidence 1 — the negative-terminal rate.** In r51–r99, eight of 48 rounds ended
in BLOCKED / UNOBSERVABLE / representation-barrier / ORACLE-INVALID with no code
(R70, R78, R81, R89, R92, R93, R98, R99). In R100–R130 the pattern intensifies:
R106 UNOBSERVABLE, R107 STOP, R109 STOP, R112 inventory-only, R115 negative
attribution, R116/R117/R118/R119 measurement-only, R123 measurement-only, R127
withdrawn, R130 withdrawn. Each carried full scope-review ceremony to reach a
one-paragraph answer.

**Evidence 2 — the two best late results were not rounds.** The
step-(d) FK recon (`r126-fk-persistence/step-d-recon-fk-chain-is-a-no-go.md`)
closed the entire FK thesis in one document. The R129 bucket-charge recon
(`r128-parity-over-throughput/r129-bucket-charge-recon.md`) settled two questions
at once — the historical Q14-flip blocker is gone, *and* there is no parity reason
to land the change — in one document, with a one-line probe. **Both were
explicitly run as cheap reconnaissance outside the cadence.**

**Evidence 3 — the cadence inflates under pressure.** By R72 the unit had become
one artefact per micro-step: R74 FORK → R75 SPIKE → R76 PROBE + P1 → R77 REPORT —
four rounds to deliver one splice. The R86–R99 Q96 campaign is **fourteen rounds
for one query**, of which R92, R93, R98 and R99 produced no code and no movement.
R100–R119 is **nineteen rounds (r114 does not exist) with zero production parity
movement**, salvaging two
genuine engine fixes (R104's grouped-USING/LATERAL bindings, R110's varchar
fidelity) as by-products.

**Evidence 4 — the commit ratio.** Roughly three quarters of the 332 commits on
this branch touch no code (87 touch a `.go` file; 104 touch anything outside
`.md`). The ceremony is the majority of the output.

**The corollary R130 and `FRONTIER.md` both reached independently:** the marginal
round is now worth less than the marginal *synthesis*. `FRONTIER.md` established
that Q4 and Q9 share one blocker — a fact derivable only by reading the lineage end
to end, and **which no individual round could see**.

## P2 — Knowledge retrieval fails, and exhortation has not fixed it

**R127** proposed fixing Q4's `aggregation-strategy` via semi-join cardinality. Its
scope was BLOCKed on **nine findings, three independently fatal**, and every fatal
one was refuted by a document already on disk in this directory:

1. `r71-q4-semi-selectivity/DIAGNOSIS.md` had already forced Q4's semi rows to four
   values and got HashAggregate at every point — *"Rows theory DEAD (third exit)."*
2. `r78-semi-selectivity/PROBE.md` had already measured the reachable bound at
   **1.28×**, not the 4.2× the scope assumed.
3. `r81-q4-ordered-remeasure/STEP0.md` had already located the divergence one rel
   higher, on a startup ratio against `stdFuzzFactor = 1.01` — the scope never
   mentioned startup, fuzz, or the tie-break.

The author had read R120–R126 and the recent commits, and **opened none of the
eight prior Q4 rounds** (R71–R74, R77–R79, R81). The scope's causal story was also
arithmetically impossible (`costAgg` gives sorted and hashed the same three tail
terms, so scaling input rows cannot flip the sign of the deficit), and its M0126
citation was stale.

**R130** then needed three revisions and was withdrawn:

- rev 1 compared a subnode total to a plan-root total, cited the **dominated
  loser**, and had its causal story refuted by artefacts **the author had already
  committed** (R128's three Q9 captures share one cost-stripped shape hash at costs
  spanning 1.31× — same-code ANALYZE drift wider than the claimed effect);
- rev 2 proposed a cut that had landed **643 commits earlier** (`2e15b8ca3`,
  2026-09-06) whose commit message says *"AND IT BUYS NOTHING"* and whose file
  carries a permanent comment headed **"WHAT THIS DOES NOT BUY"**;
- rev 3 was blocked on getting the actual row count — and the answer inverted its
  own premise.

**The decisive point is that the warning already existed.** `FRONTIER.md` §5 and
`TODO.md:6110-6112` carry a standing hazard written *before* R130:

> **125 round directories.** Before scoping round N, `ls` the directory and grep
> prior rounds for BOTH the target query AND the target mechanism. Recent-commit
> context is demonstrably insufficient.

R130 was scoped after that was written, and still needed three revisions. **A
documented warning is not a retrieval mechanism.** R130's own process rule #1 is
the same lesson one level down: *"Read the whole document — rev 2 quoted
`TODO_ALL.md:2837` and missed `:2895`, which refutes it 58 lines below."*

Contributing structural facts: `TODO.md` is **6,421 lines** and self-corrects in
place; there are **126 round directories** with no index; and the K-ledger reuses
K27 for two unrelated facts, defines K4 out of order, skips K99, and defines K26
inline in a preamble rather than in the K-list.

## P3 — The success metric is known-blind and still binds scopes

**K50 / METHODOLOGY §"BLIND"**, verified from the tool's source: `pg-plan-parity-diff.py`
parses `(cost=.. rows=.. width=..)` with one regex and moves all three into a side
column before comparison; N5 strips `::type` renderings; N6 compares Filter /
Index Cond / Hash Cond by (referenced columns, operator multiset), **not** by
literal values.

So a change to an estimate, a cost, a **width**, a cast rendering or a literal
registers **only insofar as it changes plan STRUCTURE**. The measured consequence
is stark:

| round | changed | shape-changed | category movement |
|---|---|---|---|
| R30 correlation | 99 plans | 6 | scan −1, join-method +2 |
| R31b heap density | 96 plans | 10 | qual-placement −1 |
| **R34 cast folding** | 18 plans | **0** | **none** |
| R36 baserel selectivity | 44 plans | 24 | net −2 |

**R34 corrected a 580× cardinality error and measured exactly zero**, because it
changed no plan's structure.

And `ROADMAP-to-all-match.md` §3 had established, on 2026-09-09, that no single fix
flips any query — so *"the match count rises"* is a mis-specified success test at
this stage. `handover-OC-to-CC-090910.md` §4 states it outright: **"`match` count
is NOT a success criterion."**

**Note the stronger form is false, and this document must not print it.** R35's
FINDINGS §1 wrote *"no estimate change can ever move a parity verdict"*; the table
above refutes it — R30, R31b and R36 each moved categories. The accurate statement
is the one METHODOLOGY gives: an estimate change registers **only insofar as it
changes plan structure**. R34 is the zero case, not the rule.

**And the scope clauses that survive are floors, not rises — which matters for
what should be retired.** R120's P3 was *"TPC-H match ≥ 6, none of the six flips"*
and it **correctly FAILED**, catching Q10's loss; R128's P2 was `match ≥ 2` on
TPC-DS, which was missed and is *methodology-dependent* (02 §N22). Neither is a
"the match count rises" clause. So the retirement in 04 §2.4 is deliberately
narrow: **retire "match count rises" as a success test; keep the non-regression
floor**, which is the one match-count clause the record shows working.

Two further metric hazards the record documents:

- **A MATCH can carry a category tag** (Q13 matched with `[rendering]`), so
  blocked-counts are verdict-headline counts, and a headline is not quite a block.
  `METHODOLOGY2` §3 asked for `blocked-excluding-matches` to be reported
  alongside; it is not.
- **Inter-round numbers are not mutually commensurable.** `METHODOLOGY2` §2's
  honesty ledger records that R44's start figures already differ from ROADMAP's,
  that scope changed in-window (the R41/R42 eligibility admissions), that R46's
  baseline `match=1/19` contradicts R43's `match=2/20`, and that this is why the
  recount anchors on **one fresh measurement rather than chaining round deltas** —
  chaining would compound the drift. That discipline was stated once and is not
  enforced per round.

## P4 — Instruments have decayed and produce false readings

The record contains an unusually complete catalogue of harness traps, most
self-reported. What matters is that several are *still live*.

**Still live:**

- **K18's `$$` tempfile trap.** Q36/Q70/Q86 echo psql's ERROR text containing the
  temp path, so a PID in the filename makes two byte-identical captures diff. Fixed
  at source once in 2026-09-08 and **regressed**; it produced false structural
  readings in **R122** (a reviewer counted 3 structural changes), **R123**,
  **R124** and **R128**. Currently worked around by stripping the PID.
- **Capture stamping.** Only the sweep artefact is machine-stamped with its arm;
  TPC-DS captures carry a hand-typed header and TPC-H captures carry none. R122
  §10's own words: the headline arm attribution *"rests entirely on filename
  convention."* This is exactly the gap that produced R120's B1 — a flag-OFF run
  cited as ON evidence.
- **Stats-epoch drift.** A values sweep re-samples statistics and opens a new
  epoch. R120 §6 attributed two apparent "worsenings" to drift rather than the
  flag and left a standing rule (re-take the OFF baseline; all A/B numbers
  same-epoch) that no tool enforces. The spread across R128's three committed Q9
  captures is **1.31×** of same-code ANALYZE drift — wider than the effect R130
  rev 1 was claiming, and it was produced *against* rev 1 as its refutation. R63 found a **stale** clone's carried statistics at
  201,356 against 196,849 from two **fresh** clones that agreed deterministically
  (~2.7%); autovacuum moved Q3 by −8.62 in 70 minutes (R60).
- **The TPC-DS match=1 vs match=2 discrepancy**, unreconciled and both quoted.

**Fixed but instructive** — K91 is the archetype and its lesson is general:
`launch.sh` lost its serving-binary verification (rewritten from memory) and
`capture-tpch.sh` read a wiped path, so every capture became a 4-line stub — **and
diffing two stubs reports "identical."** Both faults share one signature: **the
null result and the broken result are indistinguishable.** The standing rule
derived from it — *a harness must fail loudly, and an A/B must prove which binary
answered; where it cannot, the result is not evidence* — is the single most
valuable methodological output of the programme.

R83/R84 then narrowed it further: **the inode check is necessary but not
sufficient.** An 18-query TPC-H diff was stale-binary noise against
`tmp/goopg-oc-base`; and once a `go build` produced a binary with corpus-wide
width shifts whose cause was never determined (`go clean -cache` restored it).
*"Always verify SERVING behaviour, not just the inode."*

Other documented traps, listed so a successor does not rediscover them: the
server-level cross-session plan cache capping traced runs at **2 per query text
per server lifetime** (R54); `GOOPG_GATHER_PATHS` being a mode gate that silently
made a whole veto census 100%-uniform (R54); split winners' plan-line costs sitting
on a **different basis** than the tournament that chose them (R54); goopg's EXPLAIN
**mixing row units** (total on parallel scans, per-worker on parallel joins) where
PG is uniformly per-loop — which consumed all of R57; a run script hardcoding its
output path and **overwriting R56's baselines** (R59); `GOOPG_BIN=<base> … script
"$GOOPG_BIN"` expanding in the *outer* shell so a "base" arm served the new binary
(R65); the `-ref-port` trap producing a vacuous 22/22 (R66); a section-splitter
blind spot hiding Q44+Q54 away-moves (R66); a header-format mismatch (`=====` vs
`===`) returning `queries=0 match=0` and **reading like a clean run** (K90, R88);
`ALTER … SET STATISTICS` persisting and contaminating a later data point (R79);
live-PG Q8 oscillating between verdicts on same-day captures (R66/R67/R69); a peer
cleanup wiping `/tmp/pp2` mid-campaign (R96); and — the Go-specific one worth
remembering — `pushdown_project_arm_test.go` never running because Go reads a
trailing `_arm` as a **GOARCH constraint**, which reports as "no tests to run"
(K78).

## P5 — Gates are opted out of, and the correctness channel is under-specified

**`make plan-gate` has been a standing opt-out since R65** (R65 §2.7, R66 twice,
R69 §3.5, and R120–R124 consecutively — five rounds in a row). The justification
was reasonable at each step (20/22 differ on both binaries; re-pinning under a
renderer round would bless unrelated planner drift), and **R128 §5 then discovered
the justification itself rested on a false claim** made in two rounds: plan-gate
does not diff against live PG, it is a goopg-vs-committed-goopg baseline pin. So
the real situation is that the repo's plan pins have not been re-baselined across
~125 rounds of intentional change (02 §B12).

**`scripts/tpch-spotcheck.sh` was deferred four rounds running** (R66 twice, R68,
R69) because peer `:65433` was held. R63 then demonstrated precisely why that
matters: the spotcheck was the **only** thing that caught **Q13 returning 33 rows
instead of 34** — a wrong-results bug latent since 2026-09-03, which *that
round's gate should have caught*.

**Other named shortfalls**: R128 did not run the full `RALPH_PRECOMMIT_SCOPE=units`
scope and did not run `scripts/pg-regress-runner.sh` despite upstream cases pinning
EXPLAIN text; R126's P5 was PARTIAL (`pg_amcheck` unavailable, no real standby) and
P6 UNTESTED; R55 skipped the estimate-audit full arm and plan-gate for resource
contention; R124's values sweep ran on the **instrumented** binary.

Each of these is *named rather than hidden* — the honesty is real and should be
preserved. But the aggregate is that **the repo's stated bar has not been met for
most of this cluster**, and the reason is usually shared-resource contention, which
is an infrastructure problem with an infrastructure fix.

**And the correctness channel is genuinely under-specified relative to the plan
channel.** Three proofs (01 §F20): R56's Q78 lost three filters with checksums
still passing; R63/R64's Q13 33-vs-34; R83's Limit-below-Unique, masked on current
data by `78 < 100`. **Two real wrong-rows bugs were found by parity work, not by
the correctness gates.** That is the strongest single argument in the record for
strengthening the values channel — a values-green sweep demonstrably does not
protect qual placement, and row counts alone do not protect against
duplicate-truncation.

## P6 — Findings mutate as they propagate

Four documented instances:

1. **"41× closer to PG" → "41× more accurate."** R126's step-(d) recon wrote the
   first; R130 rev 3 silently read it as the second and built a round on it. The
   ground truth then inverted the premise entirely.
2. **"R124's increment was parity-neutral" → "the whole narrowing chain was
   parity-neutral."** Propagated into `FRONTIER.md` and `TODO.md`, and corrected
   only at R128, which had to **amend `FRONTIER.md` in place**.
3. **"plan-gate diffs goopg against live PG"** — asserted in R126's report, repeated
   in R128 rev 1, false in both.
4. **K61 → K62 and K64 → K65**: both original claims were *inferred from rendered
   numbers or from totals*, and both were wrong. The method note the pair produced
   is now standing and is the right one: **"Instrument the term, never infer it
   from the sum."**

Two structural contributors: landed REPORTs are evidence artefacts that are
appended to, never rewritten (correct), but that means an error inside one persists
unless a later round notices — R121 had to append an amendment to R120's landed
REPORT. And there is no mechanism that flags when a downstream document quotes an
upstream figure whose own caveat it dropped.

## P7 — Debt accumulates with no retirement mechanism

- **Default-off cost arms.** Four accumulated (R108, R113, R120, R121/R122); R128
  promoted one (`GOOPG_NARROW_COST_INPUTS`), leaving **three** at HEAD — one of
  which, `GOOPG_HASHAGG_WIDTH_CURRENCY`, was **resolved to *delete*** by R124 §7
  and has not been deleted. R121 §6 identified the norm ("four default-off arms is
  the limit; that belongs in the take2 charter") and explicitly *"flagged here for
  someone to move"*. Nobody moved it. Four more flags sit default-off and unlanded
  (`GOOPG_GATHER_PATHS`, `GOOPG_ONEREL_SEARCH`, `GOOPG_PARTIAL_SORT_PATHS`,
  `GOOPG_HASH_OUTER_JOIN`).
- **Ledger carry.** The same items appear verbatim across ten-plus rounds: R51
  items 2–3, R52 §4.2, R54 follow-ups, "#6", R61-#4, the NLI staleness comment,
  R55 §3 tie-break, F3 procost, the Q8 gap, AGG_MIXED. **R67 §3 noticed the
  pathology itself and legislated against it** — *"each deferred 2–3× on the same
  grounds; the grounds have not moved… the next round touching aggregation-strategy
  must re-derive #6's deferral from its own numbers"* — and the carry continued.
- **Deferral without an owner.** R81 closed Q4 with *"the owner is TBD and must be
  named at scheduling, not assumed"* and *"no round is scheduled by this
  close-out."* So that blocker has no route back in.
- **A reserved round silently skipped.** `TODO.md:4048` reserves R80 for the
  ndistinct estimator programme. **No `r80-*` directory exists**, and R81 proceeds
  as though the reservation never happened. The question was closed by R79's
  verdict (b), but the reservation was never formally retired.
- **Stale prose left inverted** by R128's promotion, filed and not fixed (02 §N18).
- **Scoping facts that decay**: `tmp/d05p2-bucket-charge.patch` is stale with three
  rejected hunks and unmeasured interaction with R128's now-default narrowing.

## P8 — One error class recurs at every level

`plan-parity-root-causes.md` §6 names it after rev 1's entire thesis collapsed:
**"read code, assumed it ran, never checked the callers."** `generateScanPaths` has
zero non-test callers. K4 generalises it: *every design must cite call sites, not
files.*

The class then recurred at every level of the programme:

- **K11(a)/(b)**: believing a comment about what the code does instead of checking
  — corrected by R3 §0 and R4 §0.
- **K17**: `addPartialHashJoinPath`'s comment claimed a SEMI/ANTI filter that
  *never existed* — the sixth stale-comment finding.
- **K19**: four separate test-side walkers could not descend a `Gather`, producing
  alarming messages (`searched tree has 0 joins`, `an ON qual was dropped, which is
  a cross product`) while TPC-H values were byte-identical the whole time. The
  failure ranked "most likely genuine defect" was this bug **both times**.
- **K22** names a *variant*: R16 wrote its caveat correctly and then asserted a
  headline that generalised past it. K4 covers concluding *without* a check; K22 is
  concluding **past a check you already performed**.
- **R123 §6**: the same shape one level down — a taxonomy artefact of the author's
  own instrumentation (`nonWhitelistedKind` is uninformative *by construction* for
  a scan path) pre-empted the tag that would have revealed the answer.
- **R130 rev 1**: a subnode cost read as a plan root.
- **R96 SLICE**: a hypothesis about the plain branch, while `innerUnique = true`
  was active throughout.
- **R71 → R72 P4**: a whole round's conclusion built on anchors that did not
  reproduce ("PG 0.23" was a selectivity misreported as a cost; "PG join rows=3"
  was **unlocated in every canonical source and discarded**), making R71's
  conclusion **unsound** — although its direction was later independently
  re-confirmed.

METHODOLOGY §7 already encodes the correct procedure — run the cheap decisive check
first; instrument the input before theorising about the logic; check callers, not
just the file; ask the oracle rather than reasoning; re-read your headline against
your own caveats. **The procedure is right. It is not enforced by anything except
the reviewer's attention**, which is why it holds in some rounds and not others.

## What the record says about the *shape* of a good round

Drawn from the rounds that went well, as a positive counterpart:

- **R6** — four pre-registered predictions, *including a negative one*, all held.
- **R42 / R124** — predictions recorded before implementing, then scored honestly
  including failures.
- **R103** — the stop rule fired correctly: a temporary fix made the target
  measurement possible but broke `JOIN … USING` controls, so **nothing shipped**.
- **R98** — ruled UNOBSERVABLE and refused to tune, invent or force, with the right
  epistemic statement: *"matching goopg's own arithmetic is not evidence that PG's
  hidden inputs agree."*
- **R121 scope** — refuted R120's pairing plan **on paper, before implementation**,
  saving a round.
- **R125** — enumerated *every* consumer of the value it was changing before
  changing it, and deliberately reversed a pin with mutation testing.
- **R95** — closed a latent wrong-results hole (`HasShareableHashJoin` not
  descending approved-NL outers) that would otherwise have shipped for two shapes.
