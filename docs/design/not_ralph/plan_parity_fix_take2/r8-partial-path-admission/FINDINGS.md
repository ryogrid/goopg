# R8 — why no partial hash-join path ever wins, and the bug that answers it

*Round 8 of `../TODO.md`. Findings round: it changes no code, because
what it found must be fixed before the change it was going to make is
safe.*

## 1. The answer: the consumer is switched off

K16 asked whether `addPartialHashJoinPath` is never called, called and
declined, or called and outcompeted. It is **none of those** — it is
called, it files, and **nothing reads the result**:

> *"ADMISSION IS OFF BY DEFAULT (`GOOPG_GATHER_PATHS`, §5 of that doc)"*
> — `gatherpaths.go:21`

`generateUsefulGatherPaths` is the only consumer of a joinrel's
`PartialPathlist`, and with the knob off it produces nothing, so every
partial path goopg builds is discarded. That is why `Parallel Hash Join`
appeared **zero** times in R7.

The knob's own comment says why it was left off:

> *"Flipping the default is a measured decision (TPC-H A/B, timing per
> moved plan) and this slice does not take it."*

The measurement it was waiting for was a **timing** one — D-05 recorded
three correct hash-join cost fixes each losing 10–22% of TPC-H by moving
the plan off the shape the post-pass can gather. **This workstream's
rule voids exactly that objection**: a plan that matches PG is not a
regression however slow. So the decision the knob was parked on is one
this goal has already made.

## 2. What flipping it does — measured, not argued

Probed with `GOOPG_GATHER_PATHS=all` on both corpora (no code change;
the knob is read from the environment):

| | off | all | PG |
|---|---|---|---|
| `Parallel Hash Join`, TPC-H | 0 | **19** | 9 |
| `Parallel Hash Join`, TPC-DS | 0 | **132** | **139** |

TPC-DS goes from zero to 132 against PG's 139. The mechanism works and
lands close to PG's own count.

Divergence categories, TPC-H: `parallelism` 18 → **15**, `join-method`
12 → 11, `qual-placement` 7 → 5, but `aggregation-strategy` 10 → **14**.
Match count unchanged at 2. So the flip is a real, mixed movement — not
the free win the raw node count suggests.

## 3. The blocker: a fail-closed assertion its author called unreachable

`GOOPG_GATHER_PATHS=all` **crashes the server** on TPC-DS Q5:

```
createPlan: parallel-aware PathHashJoin over relset 0x00000003 built a
2/1 join the executor will not run with a partial probe; the workers'
verdicts are not row-local, so the join would silently drop or
duplicate rows
```

`2/1` is `JoinTypeRight` / `JoinAlgoHash`. This is
`assertParallelAwareJoinIsRunnable` (`createplanjoin.go:613`) doing
precisely its job, and precisely as its author predicted:

> *"The moment `join_is_legal` inference relaxes the pin and a RIGHT or
> FULL join reaches here, this fires at plan-build time instead of
> returning a silently partial join."*

**The cause is a missing filter, and the assertion's own comment is
wrong about it.** That comment claims:

> *"`addPartialHashJoinPath` files only INNER hash joins today — since
> C-03b it is passed the direction's jointype and declines outright for
> SEMI/ANTI"*

`addPartialHashJoinPath` takes `jt parser.JoinType` and **never compares
it to anything** — there is no jointype test anywhere in
`joinpathsparallel.go`. The producer files whatever direction it is
handed, including RIGHT. The "declines outright" claim describes code
that does not exist.

This is the sixth stale-comment finding in this workstream and the same
class as K11a and K16's predecessor: **a comment asserting behaviour the
code does not implement.** It was caught only because the probe was run
before the default was flipped.

### 3.1 How close this came to being wrong rows

Without that assertion, the flip would have produced a RIGHT hash join
running with a partial probe — the panic text is explicit that the
result would be silently dropped or duplicated rows. The values gates
would very likely have caught it, but the assertion caught it at plan
build, deterministically, with the cause named. It is worth recording
that the fail-closed check paid for itself the first time its
unreachable branch became reachable.

## 4. Consequences

1. **The flip is blocked**, not rejected. `addPartialHashJoinPath` needs
   the jointype filter its documentation already claims — PG's
   `hash_inner_and_outer` parallel block files partial hash joins for
   `JOIN_INNER`, `JOIN_LEFT`, `JOIN_SEMI` and `JOIN_ANTI` and never for
   RIGHT or FULL, and goopg's executor is narrower still
   (`hashJoinIsPartialCapable`). The filter should be derived from that
   predicate rather than hand-listed, so the two cannot drift apart the
   way the comment and the code just did.
2. **`parallelism` is then partly addressable** — but only partly. The
   category moves 18 → 15 on TPC-H with the flip, so most of it is
   something else: worker counts (K14's page-density effect) and Gather
   placement.
3. **`aggregation-strategy` gets worse** under the flip (10 → 14). That
   interacts with K12/R9 and needs adjudicating rather than accepting.

## 5. Filed

- **R9 — jointype filter on the partial hash-join producer**, derived
  from `hashJoinIsPartialCapable`, with a unit pin per jointype and the
  stale comment corrected. Prerequisite for everything below.
- **R10 — flip `GOOPG_GATHER_PATHS`** once R9 lands: full values gates
  on both corpora, parity adjudicated per moved plan, and the
  `aggregation-strategy` regression explained before acceptance.
