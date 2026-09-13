# R128 SCOPE (rev 2, APPROVED) — take the promotion decision R124 pre-registered: `GOOPG_NARROW_COST_INPUTS` default ON

## 0. What rev 1 got wrong, first

Rev 1 framed this round as "several changes were reverted **explicitly
and only because they cost throughput**, and this goal disclaims
throughput, so the judgement inverts." **That premise is refuted by the
document rev 1 quoted.** `minimize_datum/TODO_ALL.md:2886-2890`:

> "an honest bucket size halved bucket heap but flipped Q14; **narrowing
> the cost side** fixed that and cost 10%; charging the build honestly
> cost 22% — and **the last three all failed on ONE mechanism: goopg's
> cost model has no parallel dimension**"

Those reverts lost **Gathers** (`TODO_ALL.md:4724`: "Q5/Q9/Q10 **lost
PARALLELISM**, not a build side"; `:2983` "D-05 is therefore blocked on
C-19"). A lost Gather is a **plan-shape divergence** — `parallelism` is a
first-class category in `pg-plan-parity-diff.py:98,822-828`. So the
recorded reason was a **parity** cost, which this goal does *not*
disclaim. Rev 1's whole inversion argument does not follow, and the
"reverted bundle" table is deleted rather than repaired.

This was avoidable: the failure mode is recorded verbatim in my own
project memory ("cost model has NO parallel dimension — 3 correct
hash-join cost fixes each cost +10..22% because they lose the Gather;
D-05 blocked on C-19"). I had it and did not apply it.

**What survives is smaller and stands on its own**: one flag, one
decision, already pre-registered.

## 1. The warrant — this decision was handed forward by name

`r124-nontable-leaf-widths/REPORT.md:177-184`:

> "**Keep default-off.** The promotion question is now fully informed and
> narrow: TPC-H gains two closed categories with zero regression …
> Promotion is therefore a judgement about whether a TPC-H-only gain
> justifies shipping a planner-cost change that also alters TPC-DS plan
> costs, and **it should be taken deliberately rather than as a side
> effect of this round**."

R123 (`REPORT.md:265-278`) and R122 (`:240-248`) say the same in
sequence. **R128 is that deliberate decision round, and nothing more.**

## 2. Measured this round

Live TPC-H corpus, same binary, `estimate-audit -plan-only` +
`pg-plan-parity-diff.py`:

| | match | join-method | scan-type | others |
|---|---|---|---|---|
| flag OFF (HEAD default) | 6 | 10 | 9 | join-order 14, agg 10, sort 9, param 5, qual 4, **parallelism 0** |
| **flag ON** | 6 | **9** | **8** | unchanged, **parallelism still 0** |

Two category-instances toward PG; **nothing rises, including
`parallelism`** — which is the specific hazard §0 describes, and it does
not fire for this flag. No query flips to MATCH: the headline stays 6/22.

**The capture must be committed into this directory** as R123 made
standing (`r123/REPORT.md:279-284`, noting R122's figures were
"consequently unreproducible"). Rev 1 shipped no artefact.

## 3. The flag is NOT the reverted D-05 patch — and rev 1 conflated them

Load-bearing, because rev 1's step 2 depended on the identity:

- `take3-D-05-costside-unnarrowed` (`.ralph/deferral_ledger.md:2105`) is
  the **deferral**, whose resume note says "thread a path-level width
  through pathNCols/pathAvgVarBytes" — R121 is the successor
  *implementation*, not the same code. The measured artefact is a
  separate preserved patch, `tmp/d05p3-costside-narrow.patch`.
- They behave differently, and §2 is the proof: the D-05 patch flipped
  Q5/Q7/Q9/Q10 build sides (+215.8% / +62.4% / +73.5% / +162.2%) and
  moved `join-method 12→10, scan-type 11→10`; **the flag moves 10→9, 9→8
  and raises no category.**

Therefore, **cut from this round**:

- **`MapSlotBytes` 48 → 96 (rev 1's step 2) and its P5.** The "no longer
  flips Q14" coupling was measured with the **D-05 patch** underneath
  ("Re-applying the bucket-charge patch *on top*"), not this flag.
  Re-establishing it is its own measurement. `MapSlotBytes = 48` remains
  documented "KNOWN 2x LOW … do not read 48 as validated" — a real
  follow-up, not this round's.

  **Two further reasons, from review, that make the cut decisive rather
  than merely tidy:** (a) both changes re-price corpus-wide, so bundling
  destroys attribution — a P1/P2/P3 failure with both in could not be
  assigned, and the separated A/B would have to be re-run anyway, so
  bundling can only cost a round, never save one (this is why
  `GOOPG_NARROW_UPPER` and `GOOPG_NARROW_UPPER_SORT` are two flags,
  `flaglabels.go:96-103`). (b) The bundled failure mode is **losing a
  MATCH**: `MapSlotBytes` 48→96 flipped **Q14** Hash Join → Nested Loop
  (`deferral_ledger.md:2106`), and Q14 is currently one of TPC-H's six
  MATCHes. The worst case of bundling is 6/22 → **5/22**, in a round
  whose entire warrant is "+2 category-instances, no match flip" — on
  evidence borrowed from another implementation.

  The follow-up round then starts from a clean baseline *because* of this
  cut: flag default-ON as the new floor, `tmp/d05p2-bucket-charge.patch`
  on top, one pre-registered prediction that Q14 holds MATCH.
- **Any "≈ +10%" throughput claim.** That is the other implementation's
  number. The only timing datum for this flag anywhere is
  `r124/REPORT.md:233` (SF0.25 `184s→188s`, +2.2%, attributed to census
  stderr). **This flag's SF=1 throughput cost is UNMEASURED.**

## 4. The real costs, stated

Rev 1 claimed "its only recorded cost is throughput". False —
`r121/REPORT.md:143-146` records that promotion "**re-baselines every
plan pin**". The honest ledger for a reader:

- **Gain:** TPC-H +2 category-instances toward PG. No match flip.
- **Cost:** every plan pin re-baselined (`r121/REPORT.md:143-146`).
- **Cost:** **3** TPC-DS plan shapes change — Q6, Q64, Q75 — plus Q95
  cost-only (`r124/REPORT.md:101-116`), without improving a TPC-DS
  metric.
- **Cost:** an SF=1 throughput hit, currently unmeasured for this flag.
- **Cost (standing, outliving this round):** R124's accepted
  planner-publishes-below-executor divergence
  (`r124/REPORT.md:150-157`) is today a *flag-gated* modelling debt.
  Default-ON makes it the **shipped default**, and
  `TestR124AcceptedAvgVarDivergenceOnNonTableChild` stops describing an
  opt-in arm and starts describing normal behaviour. P3 tests the risk;
  this bullet records that the debt is assumed, not discharged.

Whether that trade is worth taking is the decision. Under a goal that
disclaims runtime and counts only PG-likeness, it is — but it should be
recorded as a trade, not as a free win.

## 5. Predictions

| # | Claim | Verdict on miss |
|---|---|---|
| P1 | Default-ON reproduces §2 on TPC-H: match 6, join-method 10→9, scan-type 9→8, **no category rises, `parallelism` stays 0** | any rise ⇒ STOP |
| P2 | **TPC-DS: plan-TEXT diff OFF vs ON**, not just category counts. R124 proved the counter "**structurally incapable**" of seeing Q6/Q64/Q75/Q95-class changes (`r124/REPORT.md:101-116`). Every changed shape is enumerated and adjudicated ON-by-default; match ≥ 2; no category rises | an unexplained shape change ⇒ STOP |
| P3 | Values unchanged — **correctness is NOT disclaimed by this goal**: TPC-H spotcheck PASS, TPC-DS SF0.25 PASS=96 MISMATCH=0, **plus an SF=1 TPC-H execution pass**. The last is required because R124 pinned an *accepted* divergence where the planner publishes BELOW what the executor charges (`TestR124AcceptedAvgVarDivergenceOnNonTableChild`) — the OOM direction — and neither the quarter-scale sweep nor a Q12/Q13 row count exercises SF=1 memory (Q21 has taken a host OOM there before, per CLAUDE.md) | any value moves, or an SF=1 OOM ⇒ STOP |
| P4 | SF=1 throughput delta **measured and reported as a number**. Unmeasured for this flag today; the goal permits the cost but not the silence | unmeasured ⇒ report incomplete |
| P5 | `make plan-gate` run. R124 skipped it as a "reasoned omission, default-off flag plus P0 bit-identity on the default path" — **flipping the default annihilates that reasoning** | skipped ⇒ gate unmet |

**No prediction that match rises above 6.** §2 measured that it does not.

## 6. Hazards

- **Flipping the default is not a one-line change.**
  `narrowcostinputs.go:51-53` parses strictly opt-IN
  (`v == "1"`, read once at init). Default-ON means inverting to the
  opt-OUT convention (`v != "0"`, as `GOOPG_NARROW_BUILD` does), which
  **breaks `TestNarrowCostInputsFlagIsStrictAndDefaultOff`**
  (`narrowcostinputs_test.go:34-43`) — that test must be re-pinned to the
  new contract, not deleted. And `flaglabels.go:104-108` carries a
  comment asserting this flag "is **opt-IN** … artefacts must never
  conflate it with" the opt-OUT `GOOPG_NARROW_*` family; it must be
  rewritten or it will contradict the code.
- Regenerate `scripts/planner-flags.env`
  (`go run ./cmd/gen-planner-flag-labels`) or
  `TestFlagProvenanceEnvIsGenerated` fails.
- Check `ci/batch/` and the sweep scripts for a pinned expectation of the
  old default before flipping.
- **`FRONTIER.md` currently contradicts §2 and must be corrected in the
  same commit.** `r127-semijoin-selectivity/FRONTIER.md:54,81` lists
  "cost-input narrowing corpus-wide: parity-neutral, R124" among the
  measured-and-rejected levers. R124's *increment* was neutral; R122's
  two categories are real. Leaving both on disk recreates exactly the
  failure FRONTIER §5 exists to prevent.

## 7. Gates (FOREGROUND)

1. Suites green; `go vet`; flag env regenerated; the two flag-contract
   sites in §6 updated.
2. TPC-H parity OFF vs ON, same epoch, `estimate-audit -plan-only`
   (**never** `capture-tpch.sh`); capture committed here.
3. TPC-DS parity OFF vs ON via `capture-tpcds.sh`, `work_mem` pinned,
   **with the plan-text diff of P2**.
4. `scripts/tpch-spotcheck.sh` PASS; TPC-DS SF0.25 sweep PASS=96; SF=1
   TPC-H execution pass (P3).
5. `make plan-gate` (P5). Throughput delta measured (P4).
6. `FRONTIER.md:54,81` corrected in the same commit.
7. REPORT.md → agent review → `commit -n` + push.
