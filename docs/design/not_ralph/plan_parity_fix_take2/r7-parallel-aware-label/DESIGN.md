# R7 — carry parallel-awareness onto the plan node

*Round 7 of `../TODO.md`. Implements K15.*

## 1. Why this round, and why now

With both corpora fully parsed (R2), TPC-H's divergence categories can
finally be ranked. `parallelism` is joint-top at **18**, and **Q14
diverges on `parallelism` and nothing else** — the closest query to a
match after Q6 and Q13.

PG prints `Parallel Hash Join`; goopg prints `Hash Join`.

## 2. goopg is not missing the capability

`parallel_hash_build.go` implements a cooperative parallel hash build,
and its header states the position deliberately:

> *"PostgreSQL offers two parallel hash joins … goopg needs neither.
> Workers are goroutines in one address space, so the table can be built
> once and shared by pointer."*

So the node genuinely is parallel-aware at execution time. The label is
**accurate to print**, not a cosmetic concession — the same situation as
R2's `WindowAgg` fix, and the opposite of teaching a comparator to
forgive a difference.

What is missing is plumbing: `Path.ParallelAware` exists
(`joinpathsparallel.go:195`), `createplanjoin.go` only **asserts** on it
(`assertParallelAwareJoinIsRunnable`), and `optimizer.Join` has no field
for it — so the fact dies at plan construction and EXPLAIN cannot reach
it.

## 3. PG's rule is one generic line

`explain.c:1630`:

```c
if (plan->parallel_aware)
    appendStringInfoString(es->str, "Parallel ");
```

Not hash-join-specific: a prefix on **any** node whose `parallel_aware`
flag is set. goopg already implements exactly this for `SeqScan`, from
`optimizer.SeqScan.Parallel` (`operators_explain.go`, the "S17" arm). R7
extends the same rule to `Join`, which is where PG's own generality
already points.

## 4. The change

1. `optimizer.Join` gains `ParallelAware bool`.
2. `createplanjoin.go` sets it from `p.ParallelAware`, at the site that
   already reads that flag to assert on it — so the flag is read once,
   in one place, for both purposes.
3. The renderer prefixes `"Parallel "` when set, mirroring the `SeqScan`
   arm and `explain.c:1630`.

Not changed: any cost, any path admission, any executor behaviour. The
executor derives its parallel behaviour by walking the built tree
(`HasShareableHashJoin`, `attachParallelScan`) and does not read this
field — which is exactly why the field can be added without touching
execution, and also why `assertParallelAwareJoinIsRunnable` must keep
firing: it remains the only boundary check between the planner's claim
and the executor's own predicate.

## 5. Gates

- Unit: a parallel-aware hash-join path renders `Parallel Hash Join`,
  a non-parallel-aware one renders `Hash Join`, and the assertion still
  fires for a parallel-aware path the executor would decline.
- Suites: optimizer + executor.
- Values: both corpora. Expected untouched — this changes a rendered
  string and adds an unread field — but a values run is cheap insurance
  against the field having been threaded through a struct copy that
  something else reads.
- Parity: both corpora, live references.

## 6. Prediction, and why the round is self-verifying

**The premise is NOT verified**: I have not confirmed that Q14's chosen
path is the `ParallelAware` variant. Plumbing the flag answers the
question by construction — if `Parallel Hash Join` appears, the premise
held; if Q14 still prints `Hash Join`, its path is not parallel-aware
and *that* is the finding.

This is stated because three claims in this workstream (K11a, K11b, and
K14's numeric half) were assumed rather than measured and all three were
wrong. The design is therefore built so the measurement decides.

Predictions:
- Some TPC-H hash joins gain the prefix.
- **Q14 becomes MATCH only if** `parallelism` was its sole divergence
  *and* the label was its whole content. Both are plausible and neither
  is established; a `parallelism` category that survives the change
  means the category was carrying something else too (worker counts —
  K14's page-density effect — or `Gather` placement).
- TPC-DS: little movement expected; its divergences are multi-category.

## 7. Review record

Subagent delegation remains unavailable (`Task` not exposed; recorded
since R0). Adversarial self-review:

- **The capability claim was read from the implementation file's
  header, not inferred from the label's absence** — the distinction
  matters, because if goopg did NOT build the hash cooperatively then
  printing PG's label would be falsifying a plan, the one thing this
  goal forbids.
- **PG's rule was read at the emission site** (`explain.c:1625-1636`)
  rather than assumed from the hash-join arm, which is what revealed it
  is a generic per-node prefix and that goopg already implements it for
  `SeqScan`. That makes R7 an extension of an existing goopg rule, not
  a new special case.
- **The premise is flagged unverified in §6** rather than asserted.
