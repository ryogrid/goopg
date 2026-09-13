# R130 SCOPE (rev 3) — why does a MORE accurate Q9 cardinality produce a LESS PG-like plan?

Rev 1 and rev 2 were both BLOCKED. Rev 3 takes the question review named
as the only live, uncited, on-disk lead — and which rev 2 declined for a
reason that no longer exists.

## 0. What rev 1 and rev 2 got wrong, and the rule this earns

**Rev 1** (three errors, all mine):
1. The "5.7x stale" warrant compared **a subnode against a plan root** —
   R70's 543,226 is one hash offer's total at L6; 94,913 is the
   whole-query `Sort`. I named this as the likeliest failure when
   commissioning the review, and it was the failure.
2. 543,226 was the **dominated loser**; NLI had already won at 117,343
   (`r68-.../STEP0.md`, titled *"Q9 re-measured: NLI won everywhere"*).
   Like-for-like is **1.24x**.
3. My causal story was refuted by **artefacts I had already committed**:
   R128's three Q9 captures share the cost-stripped shape hash
   `96a7ba620076` at costs 109,733 / 124,546 / 94,913 — narrowing is
   shape-inert on Q9, and that spread is **1.31x same-code ANALYZE
   drift**, wider than the effect rev 1 claimed.

**Rev 2** proposed correcting the `avgVarBytes` over-estimate. **That cut
landed on 2026-09-06**, commit `2e15b8ca3` *"price the hash-join entry on
the narrowed build, not the table"*, 642 commits before HEAD. Its own
message says: *"AND IT BUYS NOTHING … D-04 predicted that correcting
avgVarBytes alone would take nbatch from 4 to 2. The entry does go 194 to
120; **nbatch stays 4**."* `entrywidth.go:37-48` carries a permanent
comment headed **"WHAT THIS DOES NOT BUY"**. Rev 2's P2 was built on the
one claim in D-04 that had been tested and refuted.

**The rule this earns — it is NOT R127's.** Rev 2 *did* grep the round
docs and *did* verify its numbers against committed artefacts. That was
insufficient twice over: the correcting text sat **58 lines below the
line I quoted in the same file** (`TODO_ALL.md:2837` quoted, `:2895`
corrects it), and the fix sat in the source tree with a doc comment
written to stop the round. So:

> Before scoping a cut from a dated finding: **(1) read the whole
> document, not the quoted paragraph — these trackers self-correct in
> place; (2) `git log -S` and grep the mechanism in `internal/`, because
> in this repo a landed fix's best documentation is its own commit
> message and a "WHAT THIS DOES NOT BUY" comment.**

Applied to *this* target before writing rev 3: the question below appears
in **no** round document except R126's recon, and `git log -S` finds no
mechanism commit. It is genuinely unworked.

## 1. The question

`../r126-fk-persistence/step-d-recon-fk-chain-is-a-no-go.md:50-56`
measured, on the real corpus, that declaring TPC-H's eight FKs moves Q9:

```
no FKs : [join-order]                                      rows=122   cost=109733
8 FKs  : [join-order,join-method,scan-type,qual-placement]  rows=5000  cost=374413
   PG  :                                                    rows=60125 cost=139669
```

The estimate improves **41x toward PG** (122 → 5,000 against 60,125) and
the plan gets **worse against PG on three more dimensions**. That recon's
closing line is the open question, and nothing in the tree answers it:

> "explain why a *more accurate* Q9 cardinality produces a *less* PG-like
> plan. That question is the real lead here, and it points at the cost
> model or the join-order search."

## 2. Why this is worth a round, and why it is not merely a measurement

**The uncomfortable implication, stated up front.** goopg currently
matches PG on Q9's `join-method`, `scan-type` and `qual-placement` *while
estimating the contested join 500x low*. If those three agreements
survive only because of a compensating error, then **part of the current
6/22 is accidental** — right for the wrong reason — and any future
estimate improvement will read as a regression on the parity metric.

That is a claim about the **measurement programme itself**, not about
Q9. If true, it changes how every remaining round should be judged: a
category count is then not a safe objective function, and rounds that
improve estimates would be penalised by it. Nothing else on the frontier
tests this.

**Cut reachability.** The recon's framing — corrected cardinality changes
shape, and the new shape is further from PG's — points at a
**shape-selection** defect: goopg choosing differently from PG *at the
cardinality where PG is also operating*. PG plans this join at 60,125
rows and picks the shape goopg only picks at 122. So there is a
candidate defect that is not about widths at all, and if the divergent
decision is a single comparison, it is cuttable. **This scope does not
promise that** — §4's P3 is explicitly a "name the mechanism" bar, and
§5 forbids taking a cut inside this round.

## 3. Method

The FK declaration is the knob (R125+R126 made it O(1) and durable), on a
**throwaway `cp -a` clone**, never the shared corpus — the same protocol
the step-(d) recon used.

1. Clone + capped server on `:5533`, inode-verified.
2. Capture Q9 at the two cardinalities (no FKs / 8 FKs `NOT VALID`),
   **same epoch, same connection for ANALYZE and EXPLAIN** — goopg's
   stats are per-connection, which is how the step-(d) A/B was voided.
3. `GOOPG_PGSHAPED_DP_TRACE=1` DPPATH harvest at both cardinalities
   (`joinsearchtrace.go:49`; label confirms *"diagnostic only: emits the
   enumeration trace, never changes a chosen plan"*).
4. Locate the level where the two arms part, and name which comparison
   flips: which two candidates, what costs, what margin.
5. Compare against PG's shape at 60,125 — from a **committed** PG
   capture, named in the report, not a live cluster a peer may hold.

Instrument inertness: trace-on vs trace-off byte-identical on Q9 plus
three unrelated queries, via the **cost-stripped shape-hash** method
(rev 1's one durable contribution: `96a7ba620076`).

## 4. Predictions

| # | Claim | Verdict on miss |
|---|---|---|
| P1 | The three extra categories at rows=5,000 are **attributed to specific plan nodes**, not just counted | unattributed ⇒ the round has not done its job |
| P2 | The A/B is same-epoch and same-connection, so the two arms differ **only** in FK presence — verified by the shape hash on an unrelated query | drift detected ⇒ discard and re-run; the step-(d) A/B died exactly here |
| P3 | The round **names the mechanism** by which a better estimate selects a worse shape, or states plainly that it could not and why | "it just does" ⇒ failure |
| P4 | Whether the accidental-agreement hypothesis (§2) holds is answered **yes or no**, since it governs how the whole programme reads its own metric | unanswered ⇒ the round's main value is unrealised |

**No prediction that Q9 improves, and no cut in this round.** Rev 2 was
blocked for building on an untested prediction; this one predicts only
what the measurement can settle.

## 5. Bounds

- **No cut**, and no corpus change: FKs are declared on a throwaway clone
  and the clone is deleted. The shared corpus is not touched.
- **Not a width round.** FRONTIER.md:88's claim — that targeting Q9's
  join-order without projection pushdown re-runs a known-negative — is
  **not contested**: this round targets the *estimate-to-shape* mapping,
  and its deliverable is a mechanism, not a join-order fix.
- `MapSlotBytes` and the entry model are settled and out of scope
  (R129 inert; `2e15b8ca3` landed and buys nothing).

## 6. Gates (FOREGROUND)

1. Suites green; `go vet`; tree clean at start and end (trace-only).
2. P2's drift control before any conclusion is drawn.
3. Q9 DPPATH harvest at both cardinalities + the attribution table.
4. Clone deleted; shared corpus verified untouched.
5. REPORT → agent review → `commit -n` + push.
