# Plan-parity harness — background and history (M0137–M0143)

Status: reference (non-binding). Snapshot taken 2026-09-17.

This file holds the rationale, history and known-stale-claim notes that used to
live inline in `AGENT.md` §"Plan-parity harness". On 2026-09-17, after the
second loop audit (`tmp/METHODLOGY3_RALPH_CHECK0917/`), the binding rules were
rewritten into a short section in `AGENT.md`. **Where this file and `AGENT.md`
disagree, `AGENT.md` wins.** Read this for the *why*, not for the rules.

Below is the previous section, verbatim (AGENT.md lines 496–1064 at `ac611860f`),
followed by the loop-discipline paragraph it superseded.

---

## Plan-parity harness — applies ONLY to M0137–M0143

**Scope fence.** Everything in this section applies **only** while working a task
in the plan-parity milestone group **M0137–M0143**. Outside that group,
`## Loop discipline (for Ralph)` and its design-doc rules apply unchanged. Where
this section and those rules disagree, this section wins **for these seven
milestones and nothing else**.

### The goal

Every currently executable TPC-H (22) and TPC-DS (99) query must produce **the
same plan as PG 18.3**, reached by the **same statistics**, the **same cost
computation** and the **same planning logic**. Never by forcing shapes.
**Within this milestone group only**, a slower plan that matches is **not** a
regression — execution time is reported, never adjudicated. (Outside the group,
performance regressions are judged normally; this clause must not be
generalised.)

State at the group's filing (2026-09-14): TPC-H **6/22**, TPC-DS **2/99**.

The group's immediate objective is to complete
`docs/design/not_ralph/plan_parity_fix_take2/METHODOLOGY3/04-forward-plan.md`
with the owner's two decisions applied.

### The two owner decisions (2026-09-14) — binding, and they override 04

| question | answer |
|---|---|
| **Q1** — build executor-side narrowing, or accept a ~6–7/22 TPC-H cap? | **(a) build it** (M0139). And **(c) split the goal = Go**: TPC-DS work (M0140) proceeds independently of it. |
| **Q2** — should goopg reproduce PG's estimation errors? | **YES** — *"because otherwise identical plan generation is impossible."* (M0138) |

Two things follow that a reader of 04 alone would get wrong:

- **04 §1.2's proposed `PARITY-BLOCKED-BY-ORACLE-ERROR` rule was put to the
  owner and REJECTED.** Do not implement it and do not cite it.
- **R79's verdict (b) — "keep the superior statistics, close Q4 elsewhere" — is
  OVERTURNED.** Do not cite it to decline sampling work.

**Q2 is not a licence to fudge.** The goal statement already requires the
**same statistics**, so goopg's divergent ANALYZE sampler is a
PG-incompatibility and removing it is faithfulness work of the same kind as
porting a cost term. The method is: **port PG's `acquire_sample_rows` and let
its output be whatever it is.** Never tune a constant toward a target number,
never special-case a query, never add a fudge factor to "reach" PG's estimate.
**A task whose diff contains a constant chosen to make an estimate match is
rejected.** A number matched by tuning proves nothing about the mechanism and is
exactly the arbitrary forcing the goal forbids — the same reasoning K92 applies to a
mislabelled plan node, one layer down.

### Further owner decisions (2026-09-15) — binding

Taken after the 2026-09-15 progress review (`tmp/METHODLOGY3_RALPH_CHECK0915/`),
which found 36 tasks completed with the goal metric unmoved (TPC-H 6/22,
TPC-DS 2/99) and TPC-DS categories net **worse** (525 -> 540).

#### B1 — `minimize_datum` / packed retention: **NO-GO**

M0139-0006 escalated the go/no-go. **The answer is NO-GO.** `minimize_datum`
stays NOT APPROVED TO START and is **out of scope for M0137–M0143**. Do not
propose it again inside this milestone group; do not write tasks that depend on
it. If a task's only remaining lever is packed retention, the task is blocked
and says so in a ledger row.

Rationale, from M0139-S3's own measurement: the post-pushdown residue is
**128.4 B/row** against PG's 22 B/row, but shrinking entry bytes does **not**
reliably shrink the batch count (`entrywidth.go`'s measured non-monotonicity —
194 -> 120 B/row leaves `NBatch` at 4, and 63 B/row returns to 4 because
`MapSlotBytes` dominates). Packing alone closes ~5x of a ~48x gap, and its goal
is **byte parity, which is not this group's currency**. This group's currency is
**plan structure**.

#### B2 — same statistics, same plan: **direction unchanged**, plus a new obligation

The goal is unchanged: goopg reaches PG's plan through **the same statistics and
the same costing**, and Q2's "reproduce PG's estimates" still stands.

**But there is a new, binding obligation.** goopg and PG differ irreducibly in
places fixed by implementation language and foundational design — `Datum` is
48 B where PG's MinimalTuple is ~22 B; a Go `map[K][]Row` carries per-bucket
overhead PG's pointer array does not. Feeding those goopg-native quantities into
PG's cost formulas is **not** faithfulness; it is feeding PG's arithmetic the
wrong inputs, and it is why identical statistics have not produced identical
plans.

> **Absorption principle.** Where a divergence is irreducible, the cost model is
> given the **PG-equivalent logical quantity**, while the executor allocates
> whatever the Go implementation actually needs. The two currencies are
> separated deliberately, and the separation is pinned by a test.

This is **not** tuning, and the distinction is the one that matters:

| | what it does | verdict |
|---|---|---|
| **Tuning** (forbidden) | bends a goopg number until the *output* matches PG's | rejected — proves nothing about the mechanism |
| **Absorption** (required) | gives PG's *formula* the same *input* PG would have | faithfulness work, same kind as porting a cost term |

A worked example, and the reason this unblocks the width programme: PG's
`cost_hashjoin` decides spill from a width expressed in PG's tuple
representation. Handing it `48 * ncols + 24 + avgVar` makes goopg spill where PG
does not, and the plans diverge. Handing it the PG-equivalent width makes the
**spill decision** match — goopg may still really spill and run slower, and
**that is explicitly acceptable**: a slower plan that matches is not a
regression. Plan parity does not require goopg's memory footprint to equal PG's.

Consequences to carry:
- This, not packed retention, is the route for the M0139 width programme. It is
  filed as **M0139-0007** and is a prerequisite reading for M0141-S2a-fix.
- An absorption site must be **named and justified in its design doc** (which
  irreducible difference, which PG quantity is substituted, why the substitution
  is the PG-equivalent and not a fitted constant) and **pinned by a test** that
  fails if the two currencies are silently re-merged.

Two rules make the tuning/absorption line operational rather than a matter of
self-assessment. Both are binding; a task that cannot satisfy them is **blocked,
not absorbed**.

1. **Derive before you measure.** The substituted quantity is fixed by
   derivation and written into the design doc **before** any parity number is
   taken. Writing several candidate expressions and keeping the one whose parity
   result looks best is tuning by another name, whatever the expressions are
   called. If a first derivation turns out wrong, say so in the doc and redo the
   derivation — do not search.
2. **The quantity must exist in PG.** An absorption is a **port of a named
   function or expression under `./postgres/`**, and its design doc cites the
   `file:line`. If PG has no expression that computes the quantity — a Go-only
   cost such as GC pressure, interface dispatch, or `MapSlotBytes` bucket
   overhead has no upstream counterpart — then there is nothing to absorb *to*,
   and the right outcome is a ledger row saying the divergence is unabsorbable,
   not a number chosen by the author.

Two things this principle does **not** licence:
- It does **not** apply to C3/K63, the display seam where EXPLAIN renders a scan
  cost the planner never used (M0137-0015). That is an **instrument defect** and
  must be repaid, not absorbed. Absorption separates two currencies on purpose;
  C3/K63 reports one currency under the other's name by accident.
- R124's accepted "planner publishes below what the executor charges" divergence
  *is* an instance of the principle and not a debt to repay — but note why it is
  safe: `r128-parity-over-throughput/SCOPE.md:137` calls it "the OOM direction",
  and it is acceptable only because the SF=1 TPC-H execution path is a mandatory
  gate (`scripts/tpch-acceptance-arm.sh`, all 22 queries). An absorption that
  errs the other way — planner charging below what the executor needs — must
  carry that same execution gate before it lands.

#### B3 — `GOOPG_GATHER_PATHS=all` default: **keep (a)**

M0140-0003's flip stays landed even though TPC-DS categories went 525 -> 540.
Rationale: `match` held at 2, the values sweep was all-zero, the flip carried a
genuine bug fix (`clonePlanReplacingOuter`'s missing `*Gather` arm, which had
been silently swallowing an error and leaving TPC-H Q2 un-decorrelated), and the
category rise is most plausibly newly-*visible* divergence in plans that only
now go parallel. **This is a recorded decision, not an oversight** — a later
task may not revert it on category count alone; reverting requires new evidence
that the flip itself (not the visibility it created) causes the rise.

### Design docs — timing override for this group

**Write the design doc when the task is selected**, not before the milestone
starts: `docs/design/0100-0149/<task-id>-<short-slug>.md` (lowercase task id, in
the numbered bucket directory — this is the repository's actual convention and
what the first 38 docs followed), status `draft` -> `accepted`, indexed in
`docs/design/README.md` **in the same commit**. Slice ids (`-s1`) and letter
branches (`-0003a`) are legitimate task ids; they need no `NNNN` sequence. This follows the
M0134 precedent and **overrides** three rules for this group only:
`docs/milestones/README.md` §"Workflow Per Milestone" step 2 ("write the design
docs listed under Required Design Docs first"), this file's own "reserve a
concrete design-doc filename before coding", and `.ralph/PROMPT.md`'s
"Reserve a concrete design-doc filename before coding". There is no up-front
Required-Design-Docs list for M0137–M0143.

The same-loop, same-commit indexing requirement is **not** relaxed.

### What to read, and what not to read

The previous phase left **126 round directories** under
`docs/design/not_ralph/plan_parity_fix_take2/` (`r0-*` … `r130-*`). They are raw
evidence, not guidance, and browsing them is how R127 was withdrawn on nine
findings, three fatal, **every one refuted by a document already on disk there**.

Read in this order, and stop when answered:

1. `METHODOLOGY3/README.md` — goal, current score, the frontier, the decisions.
2. `METHODOLOGY3/01-what-we-learned.md` — the 20 durable findings, plus Part C
   (claims that were established and then refuted).
3. `METHODOLOGY3/02-open-problems.md` — blockers, correctness risks, the
   numbered `N*` items the milestone tasks cite.
4. `METHODOLOGY3/03-process-retrospective.md` / `04-forward-plan.md` — method
   and plan, **read with the decisions above applied**.
5. The milestone doc under `docs/milestones/01NN-*.md`.

**Enter a round directory only via `INDEX-by-query.md` / `INDEX-by-mechanism.md`
(built by M0137-0008), via an explicit citation in METHODOLOGY3, or via a path
named in an M0137–M0143 task line** (the third exception exists because
M0137-0001 must open `.../r2-instrument/` to move the capture scripts, and the
index does not exist until the eighth task). Never browse
`plan_parity_fix_take2/` to find out what was tried — that is what the index is
for, and citing it is a scope gate.

`METHODOLOGY3/README.md`'s "Review record" lists what an adversarial review
already corrected in those documents; read it before treating any METHODOLOGY3
claim as novel.

### Completion rule for this group — a deferral needs TWO artefacts

**Binding for every M0137–M0143 task.** A ledger row records *what* was
deferred; a `.ralph/fix_plan.md` `[ ]` task records *who owns it next*. Closing
a task with only a ledger row leaves the mechanism an orphan — the 2026-09-15
review found **eight mechanisms** in exactly that state, with no executable task
anywhere in the tree.

- Deferring any part of a task requires **(a)** a `.ralph/deferral_ledger.md` row
  with a concrete resume point **and (b)** an unchecked `.ralph/fix_plan.md` task
  under the milestone that will finish it. Both, or the task is not complete.
- Wherever a milestone's Definition of Done says "*or* its absence is a filed
  ledger row", read it as "**and** a filed follow-up task". The earlier wording
  made "write a ledger row" a legitimate way to close an *implementation* task;
  it is not.
- If the follow-up genuinely belongs to no milestone in this group, say so in the
  ledger row's `why` column and name where it does belong.
- A **recon** task is the one exception, and only because its whole deliverable
  is the filing: it completes when the measurement, the design note and the
  follow-up tasks all exist.

`docs/milestones/0137-*.md` and `0140-*.md` restate this rule locally; this
section is the authority, and it binds the five milestone documents that do not.

### The deferral ledger — how to read it for this group

`.ralph/deferral_ledger.md` carries ~1,980 rows with `status = -`. Three things
to know before mining it:

- **`status` is not reliable.** Several `-` rows describe work that has since
  landed — `take2-P1-16` (LIKE pattern selectivity) is marked open while
  `internal/optimizer/patternsel.go` implements it and `selectivity.go` consumes
  it; `M0127-P5.5-e-ii-b` (Memoize path/rescan cost) is open while
  `joinpathsmemoize.go:125,216` has it. **Re-verify against the tree before
  scheduling a row.** `git log -S <the mechanism>` is the cheap check.
- **Most of it is out of scope.** Roughly three quarters is WAL/replication,
  catalog/DDL, MVCC/locking, types/codec, parser and test harness — none of it
  reaches plan selection. The planner-relevant rows cluster in the take2/take3 and
  M0137–M0142 band near the end of the file.
- **A closed milestone can leave live rows behind.** M0137–M0140 are all closed,
  and the rows they filed had no follow-up task until 2026-09-15. When a ledger
  row names a mechanism that matters for parity and no `[ ]` task owns it, that
  is a filing bug — raise it rather than assuming someone declined it on purpose.

### Known-stale claims — do not act on these

Each of these is contradicted by the tree at HEAD or by a later measurement:

- **K26 §9.3** describes the join-search seam as constants-only. It is a
  **pre-R51 snapshot**; `joinsearchseam.go:461` calls
  `inferTransitiveEqualities` unconditionally and both Slice-3 tests were
  adjudicated against PG. Only the **costing** half of join-order is open.
- **R35 FINDINGS §1** — "no estimate change can ever move a parity verdict" is
  **false**. R30, R31b and R36 each moved categories. The accurate statement is
  that an estimate change registers only insofar as it changes plan *structure*.
- **The R43-era "5 failing tests, 4 needing adjudication" list** is ~87 rounds
  stale; at least one member was re-baselined by R51. Re-measure (M0140-0001).
- **`bench/tpch/plans-pg/`** is a stale, serial fixture and is **not** a parity
  target (K9). The TPC-DS sibling's standing is M0137-0004's subject.
- **"Four default-off cost arms"** — there are **two** at HEAD:
  `GOOPG_PG_HASH_TUPLE_SPILL_COST` (R108) and `GOOPG_PG_SORT_RELATION_BYTES_COST`
  (R113). R128 promoted `GOOPG_NARROW_COST_INPUTS` to default ON, and M0137-0009
  deleted `GOOPG_HASHAGG_WIDTH_CURRENCY` per R124 §7's promote-or-delete
  resolution (the pairing hypothesis it was held for was refuted by measurement).
  **The cap itself (R121 §6, previously "flagged for someone to move" and never
  moved): a default-off cost arm is evidence, not a standing feature — past about
  four of them accumulating at once they become debt, and each one carries an
  explicit expiry (a measurement or a landed consumer) that resolves it to
  promote or delete.** This is now that norm's home; do not let a new cost arm
  join without one.
- **`METHODOLOGY.md` §2 and `ROADMAP-to-all-match.md` §1–2 numeric tables** were
  superseded by `METHODOLOGY2.md`, which was superseded by `METHODOLOGY3`.
- **`04-forward-plan.md` §1.2's proposed rule** — overruled by Q2 above.
- **The parity verdict has no `rows=` dimension.** Its nine categories are
  join-order, join-method, scan-type, parameterisation, aggregation-strategy,
  sort-strategy, parallelism, qual-placement, rendering. A wrong row estimate is
  invisible to every parity gate and reaches the metric only indirectly, by
  changing plan shape. Do not read "no category movement" as "estimates are
  fine". `make ea-ratchet` (`Makefile:616`) is the instrument that scores them
  directly; it exists and runs — M0137-0018 re-scores it at HEAD.
- **"TPC-DS row estimates are three to five orders out"** (Q22 9,460,201 vs PG's
  11,987; 22 of 100 Sort inputs estimating 1) is a **2026-09-06 measurement
  taken before its own fixes landed**. All four cuts the diagnosis named — B1
  (`joinkeyproof.go:248`), A1 (`rangequery.go:211-215`), A2
  (`selectivity.go:322`), A3 (`reduce_outer_joins.go:141`) — have since landed.
  Nobody has re-measured. Quote no figure from that row; M0142-0004 is the
  re-measurement.
- **`take3-ea-ratchet-never-ran`** is superseded by a `resolved` row for the same
  id thirteen lines below it. This is the general shape of the trap: **the ledger
  can hold several rows with the same id and only the last one is current.**
  `grep -n '<id>' .ralph/deferral_ledger.md` and read the **highest-numbered**
  hit before scheduling anything from it.
- **`make plan-gate` does not diff against live PG.** It is a
  goopg-vs-committed-goopg baseline pin (`Makefile:431-453`). The claim that it
  cannot pass until the goal is met appears in two round reports and is wrong.

### Way of working — this is a Ralph loop, not the R-round cadence

The previous phase ran an interactive cadence: SCOPE -> agent review -> commit ->
implement -> REPORT -> review -> commit, one numbered round per step. **Do not
reproduce it.** Concretely:

- **Do not create `rNNN-*` directories.** New raw artefacts go under
  `analysis/m01NN/`; conclusions go in the task's design doc.
- **Commit the raw artefacts, in the same commit as the design doc that cites
  them.** A measurement whose evidence sits in `/tmp` or untracked is not
  reproducible and the next task will re-run it. At the 2026-09-15 review the
  primary evidence for the *current* metric values (`analysis/m0142/`) and every
  file of `analysis/m0138/` were untracked; a design doc's prose summary is not
  a substitute (K90: anything needed across sessions lives in the repo).
- Ralph's own discipline applies: **one task per loop**, the working-set baton,
  the pre-commit gates, `make ralph-state-guard` before the status block.
- 04's two-tier model maps onto tasks, not rounds. A **recon task** is
  measurement plus a design note plus (where something is deferred) a ledger row,
  with **no production change** — `M0138-0001`, `M0141-S0` and `M0142-0001` are
  recon tasks, and a production diff in their commit is a scope violation. Every
  other task is an implementation task and lands its own gates.
- The two most productive results of the previous phase were cheap recons run
  outside the heavyweight cadence. Prefer eliminating a hypothesis in one task
  over scoping a campaign around it.

### Working-directory hygiene, gate honesty, commit labels

The first three bullets below are **repository-wide** — they are placed in this
section because this milestone group is where they were broken, not because they
stop applying outside it. The `GOOPG_SKIP_PRECOMMIT` bullet is the one that is
group-scoped, and says so.

- **Never create a directory at the repository root.** Scratch goes in
  `analysis/m01NN/`; throwaway files go in the session scratchpad. `bak/` once
  held a root-level `package executor` test file and broke `go vet ./...` for
  ten hours while **five consecutive tasks accepted the red as "pre-existing,
  unrelated debris"** — debris the loop's own window had created. `/bak/` and
  `/estimate-audit` are now gitignored; that is a backstop, not a licence.
- **Never commit a build artefact.** A 9 MB `estimate-audit` binary reached the
  repo root via a "commit Ralph loop WIP" sweep. Build to `bin/` or `tmp/`.
- **A red gate is never "pre-existing" until you have proved it.** Before
  accepting one, run it at `HEAD` with your change stashed. If it is genuinely
  pre-existing, say so **with the command and its output**; if it is yours, fix
  it. "Unrelated concurrent work" is a hypothesis, not a finding.
  One red *is* already proved, so nobody need re-prove it: `go vet ./...` reports
  two `lostcancel` findings in `cmd/goopg/main.go:837` and `:881`
  (`cpCancel` not used on all paths). Verified pre-existing at HEAD on
  2026-09-15 and unrelated to this group; every other package is clean. Any
  **third** vet finding is yours.
- **Do not bypass the pre-commit hook.** `--no-verify` is forbidden and leaves a
  trace; `GOOPG_SKIP_PRECOMMIT=1` leaves **none**, so it is forbidden outright
  for M0137–M0143. If the smoke cannot run, that is a blocked task with a ledger
  row, not a skipped gate.
- **The commit `area(scope):` prefix must reflect the largest surface touched.**
  A commit that edits `internal/` is not `docs(...)`, even when the edit is only
  comments — the loop's own audit trail is read by `area`.

### Measurement — pipeline, ports, and what the metric cannot see

**Bootstrap first.** `psql`, `pgbench`, `pg_isready` and `pg_ctl` live ONLY in
`./postgres/local_install/bin/`, never on a default PATH, and every bench port
has its own user/password. The exports, the port->db/user/password table and
the working `estimate-audit` / `capture-tpcds.sh` / `pg-plan-parity-diff.py`
command lines are in
`docs/design/0100-0149/m0137-0003-baseline-capture-procedure.md` (M0137-0003
— **read it before running anything**), including `make plan-gate` (without
the PATH its `pg_isready` probe fails as *command not found* and the gate
misreports the server as unreachable). That doc superseded the old
"plan-parity-take2 work appendix" this section used to point at (deleted by
the commit that filed M0137-0003, on the expectation this task would replace
it); where a round-cadence framing anywhere conflicts with §"Way of working"
above, that section wins.

**Canonical captures** live in `scripts/` after M0137-0001; the M0137-0003 doc
above is the one documented procedure for using them. Two facts that have
each cost a round:

- **TPC-H baselines come from `estimate-audit -plan-only`, not
  `capture-tpch.sh`** — the latter opens a fresh session per query and never
  ANALYZEs, so it captures TPC-H plans on **empty statistics** (goopg's ANALYZE
  results are per-connection).
- **`-serial` defaults to `true`** (`cmd/estimate-audit/main.go:285`), setting
  `max_parallel_workers_per_gather = 0` on **both** engines. That is why TPC-H
  `parallelism` reads 0: the category is measured **out**, not solved. The last
  real reading was 18->16 under the gather-paths flip.

**Ports** (see also `CLAUDE.md`): PG TPC-H `:65432`, goopg TPC-H `:65433`, goopg
TPC-DS SF0.25 `:65437`, PG TPC-DS `:65438`. Throwaway servers use `55xx` with a
private data clone. The `:6543x` block is shared — **verify a reference, never
restart it.**

**Server traps** (each earned): verify the serving binary by inode **and** by
behaviour — the inode check alone is insufficient, and a stale `tmp/` binary
once produced a false 18-query diff. `pg_isready` READY is necessary, not
sufficient: a surviving older server answers the port while your instance never
binds. **Never `pkill -f goopg`** (it self-matches the invoking shell, exit 144).
Always go through the cgroup cap wrapper (`scripts/goopg-test-run.sh`).

**What the parity verdict is blind to (K50).** `scripts/pg-plan-parity-diff.py`
normalises `cost=`, `rows=` and `width=` out before comparing, strips `::type`
renderings, and compares quals by (columns, operator multiset) rather than by
literal values. **An estimate, cost or width change registers only insofar as it
changes plan STRUCTURE** — R34 corrected a 580x cardinality error and measured
exactly zero. This is why a cost or statistics task is judged by category
movement plus `shape-delta`, never by estimate movement alone.

### The toward-oracle hazard — read before M0138 and M0142

Moving an estimate or a cost **toward** PG is the change class that has already
produced the programme's worst runtime regressions, because goopg's executor
does not have PG's mitigations:

- **B6** — R59 repriced index probes toward PG's constants, a correct and
  PG-faithful change, and TPC-DS **Q72 went from 4 s PASS to a 320 s TIMEOUT**:
  goopg has no Memoize on the NL probe path PG plans that shape with. Carried
  unfixed since. Expect this class, do not be surprised by it.
- **B8** — `indexProbeCostMultiplier = 2.0` deliberately departs from PG because
  goopg's executor materialises the whole TID list eagerly. **At `mult = 1` the
  DP picks PG-shaped NL plans that run 2–3x slower** (Q7 5.86 s -> 15.72 s). This
  is the known parity-vs-runtime knob; touching it is a cross-layer programme
  that has never been scoped, and K73's `Join.FromOuterReduction` is its sibling.
- **B10** — `indexCorrelationFor` returns 0 when the leading column has no
  correlation slot, pricing **every** such index scan at `max_IO_cost`; and
  R30's residue is that ANALYZE never visits indexes, so `estimateIndexGeometry`
  **synthesises** relpages/reltuples/tree_height. Both shift index-probe pricing
  corpus-wide and both sit directly under M0142. M0138-0004 populates
  correlation slots as a side effect — re-measure these two before concluding
  anything about index-scan costs.

**Time every query whose plan changed**, and file a ledger row for any that
regresses. Per the precedence rule below, a slower matching plan lands; an
unmeasured one does not.

### What every M0137–M0143 task report must contain

In the loop's report and in the task's design doc.

**Every item is mandatory, and `N/A` is an allowed answer — silence is not.**
A task that runs no corpus capture has no shape-delta and no stats epoch; write
`N/A — no corpus run this task` against those items. An omitted item is a
report defect, an explicit `N/A` is not. (This exemption exists because 11 of
the first 22 tasks were pure recons that silently dropped items they could not
physically produce.)

1. **Category movement**, as the **two lines the tool prints**: `CATEGORIES:`
   (raw) and `CATEGORIES-EXCL-MATCH:` (MATCH verdicts excluded). Paste both
   verbatim from `scripts/pg-plan-parity-diff.py`; do not re-derive either by
   hand. A MATCH can carry a category tag, so the raw line overstates "blocked"
   — the second line is the honest one. (Before 2026-09-15 this item asked for
   a `blocked-excluding-matches` figure no tool produced, and 0 of 22 tasks
   could report it. The tool now emits it.)
2. **`shape-delta` counts.** A task with `shape-changed = 0` moved no plan at
   all; one with shape changes and no category movement moved plans **sideways**.
   Conflating the two produced a wrong conclusion once already.
3. **The declared stats epoch** for both arms, and confirmation that the OFF
   baseline was re-taken if a values sweep intervened.
4. **A seam-decline census by class, not by total**, at a stated timeout — a
   class can be *converted* rather than removed, and censuses are only
   comparable at equal timeouts. **There is no tool for this yet**: produce it
   by hand from `GOOPG_PGSHAPED_DP_TRACE=1` output
   (`grep -oP "seam-decline reason=\\K\\S+" <log> | sort | uniq -c`), or write
   `N/A` when the task ran no traced capture. Automating it is
   **M0137-0014**.
5. **Which planning route the query took** — the PG-shaped path search, or the
   legacy/prebuilt constructor. `tryJoinSearch` preserves the syntactic node when
   `tryPGShapedJoinSearch` declines, and forced `join_collapse_limit=1` forms
   never reach the path-cost seam at all. A whole class of experiment was
   invalidated by measuring the wrong route.

### Success criterion

**Do not write a success test of the form "the match count rises."** No single
fix flips a query: at the group's filing, non-matching queries differed from PG
in several categories at once. Progress is **category movement**.

**Keep the non-regression floor**: TPC-H match >= 6, and TPC-DS match >= 2
(Q9, Q41) — the canonical figure **M0137-0004** declared, against live PG
`:65438` via `scripts/capture-tpcds.sh`
(`docs/design/0100-0149/m0137-0004-tpcds-match-reference-reconciliation.md`).
The `match=1` reading some earlier round reports quote does not reproduce at
HEAD (measured against both live PG and the committed `bench/tpcds/plans-pg`
fixture, 2026-09-15) and should not be requoted as a live alternative. None
of the current matches may be lost. That floor is the one match-count clause
the record shows earning its place — R120's caught the loss of Q10.

**Measure the floor inside the task that changes production code**, not three
tasks later. M0138-0002 and M0138-0004 replaced the ANALYZE sampler and landed
on `go test` alone, with the floor confirmed retroactively by M0138-0005 (it
held, but that was luck, not process). Deferring the measurement is allowed
only with a ledger row naming the task that will take it.

**The floor measurement, by script name** — the same defect that made the values
gate unciteable would otherwise repeat here:

| corpus | capture | score |
|---|---|---|
| TPC-H | `./bin/estimate-audit -plan-only` per `m0137-0003-baseline-capture-procedure.md` (**not** `capture-tpch.sh` — empty-stats trap) | `scripts/pg-plan-parity-diff.py`, read `MATCH` count and `CATEGORIES-EXCL-MATCH:` |
| TPC-DS | `scripts/capture-tpcds.sh` against goopg `:65437` and PG `:65438` | same |

**The estimate-accuracy instrument — `make ea-ratchet`, a third table, not a
substitute for either above.** Neither table above can see estimate quality:
`pg-plan-parity-diff.py` normalises `rows=` out entirely (K50), and the values
gates check result correctness, not cardinality error. `make ea-ratchet`
(`scripts/estimate-parity-gate.sh`, C-20a) is the only instrument that scores
goopg's `EXPLAIN ANALYZE` estimate against its own actual row count, PG-relative,
over TPC-DS SF0.25 — the corpus Q2 ("reproduce PG's estimates") and M0138/M0142
estimator work should consult before and after a statistics/selectivity change.
It is **not** required by any task in this group by default; cite it explicitly
when a task's claim is about estimate accuracy rather than plan shape.
`EA_CAPTURE=<file> make ea-ratchet` re-scores an existing capture with no
server. **Re-pin the baseline (`make ea-ratchet-repin`) whenever the underlying
corpus changes** — M0137-0018 found the 2026-09-07 baseline had silently gone
stale across the SF0.5→SF0.25 dev-gate migration (`e2a50de40`, 2026-09-11): the
goopg-side data *and* the `bench/tpcds/plans-pg` PG fixtures moved together, but
`ea-baseline.txt` did not, so every ratchet run between the two dates was
comparing finding identities across two different corpus scales. Current
baseline: `analysis/planner-refactor-take3/c20a-estimator-census-20260915/`
(140 findings, commit `4c5c13905`).


**The values gates, by script name** (the earlier wording said "TPC-H digest"
without naming a script, and every implementation task silently substituted a
different one):

| corpus | canonical gate | bar |
|---|---|---|
| TPC-H | `scripts/tpch-spotcheck.sh` | canonical row counts (Q12/Q13), not "no error" |
| TPC-H (full) | `scripts/tpch-acceptance-arm.sh` | digest byte-identical to the baseline arm — **required when the task changes statistics, costing or the executor**; the spotcheck alone is not sufficient for those |
| TPC-DS | `scripts/tpcds-sf025-regression.sh sweep` | `MISMATCH=0 CKMISMATCH=0 ERROR=0 TIMEOUT=0` |

**A values break stops the task.**

**These gates cost more than one loop, and that is expected.** The full TPC-H arm
plus both floor captures is roughly an hour of wall clock, against a 2026-09-15
measured average of ~17 min per completed task. A task that must run them is
**explicitly exempt from finishing in one loop**: land the code with the gates
still running, or split the measurement into its own immediately-following task
named in the report. What is *not* allowed is substituting a cheaper gate and
arguing it covers the same risk — that is the escape route D3 closed.

**When a values gate cannot RUN** (shared port busy, missing data dir, peer-held
server), it is not a pass. Produce all three of: (i) the exact command and the
failure text, (ii) which substitute gate you ran instead and why it covers the
same risk, and (iii) **a deferral-ledger row promising the re-run, naming the
task that will do it.** A skip without that ledger row is a report defect.
M0139-S1 and M0139-S2 landed planner changes with TPC-H values unverified on a
self-made substitute argument and no ledger row; that is the hole this closes.

**The specific cause of those two skips is fixed (2026-09-15).** The private-lane
clone used to wait for the shared `:65433` to stop before `cp -a`, which never
happens — that cluster is resident. `scripts/lib/tpch-private-clone.sh` now takes
an **online `pg_basebackup` clone against the live server** (goopg implements the
server side: `internal/backup/basebackup.go`), so no gate needs the shared server
stopped. Verified with `:65433` up throughout: 1.9 GB cloned in 8–25 s and
`scripts/tpch-spotcheck.sh` returning `PASS` (Q12=2, Q13=34) in 39.9 s — the run
M0139-S1 could not complete in three attempts over fifteen minutes. Consistency
is guaranteed by the backup protocol (IMMEDIATE checkpoint -> start LSN,
`-X fetch` ships the WAL, `pg_control` written last), not by hoping the source is
idle; it was tested against a cluster taking 200k concurrent INSERTs and the
clone recovered to a clean committed prefix. `TPCH_CLONE_MODE=copy` restores the
old offline behaviour for a lane that must not perturb the source at all.

**Precedence when a matching plan is slow enough to time out.** These two rules
collide, and the collision is not hypothetical — B6 records a *toward-oracle*
repricing taking TPC-DS Q72 from 4 s to a 320 s TIMEOUT, and M0138 and M0142 are
exactly that class of change. The precedence is: **a matching plan that times out
is not a parity regression, but it IS a coverage loss.** Land the plan, file a
ledger row naming the query, the timeout and the suspected executor gap, and
report the query in `Gates run:`. **Do not raise the timeout to hide it**, and do
not revert a PG-faithful change solely because it got slower — execution time is
reported, never adjudicated. A values *mismatch* (wrong rows) is a different
thing entirely and always stops the task.

---

## Superseded loop-discipline paragraph (AGENT.md 447–458 at `ac611860f`)

- **As of 2026-09-14 the banner ranks the plan-parity group M0137–M0143 first**
  (M0137 -> M0138/M0139/M0140 -> M0141/M0142 -> M0143). **M0143 is gated on
  nothing — select it whenever everything above it is blocked**, which is what
  prevents a stall when M0141/M0142 expose only their recon task. Below the
  group come M-NIGHTLY's own items, then the pre-existing milestones.
  **M-NIGHTLY's *filing* obligation is unchanged and still unconditional** —
  every loop reads `ci/logs/action-items.md` and files each new `## AI-`
  subject — but M-NIGHTLY items are no longer *selected* ahead of the group.
  Two carve-outs still preempt: an item that breaks the build, and an item that
  breaks a gate the group depends on. Before selecting any M0137–M0143 task,
  read §"Plan-parity harness — applies ONLY to M0137–M0143" below; it is
  binding.

## Superseded fix_plan banner (`.ralph/fix_plan.md` 3–117 at `ac611860f`)

### Current Priority

Roadmap derived from `.ralph/specs/GOAL_AND_REQUIREMENTS.md` (§10 "Definition of
Done (Initial Milestone)"). Pick the topmost unchecked item **unless this banner
or a dependency forces another order**. **This banner is the sole ordering
authority** — `.ralph/working_set.md`'s "NEXT LOOP" note carries state, not
priority, and does not outrank it.

### Selection order (rewritten 2026-09-14, user directive)

**The plan-parity milestone group M0137–M0143 is the highest-priority work.**
Its goal is the one in `.ralph/specs/GOAL_AND_REQUIREMENTS.md`: every currently
executable TPC-H and TPC-DS query produces **the same plan as PG 18.3**, reached
by the same statistics, the same costing and the same planning logic — never by
forcing shapes. The group completes
`docs/design/not_ralph/plan_parity_fix_take2/METHODOLOGY3/04-forward-plan.md`
with the owner's two decisions of 2026-09-14 applied.

**Before selecting any M0137–M0143 task, read `AGENT.md` §"Plan-parity harness —
applies ONLY to M0137–M0143".** It is binding and it carries the goal, the two
owner decisions, the reading order for the prior phase's evidence, the list of
known-stale claims, and what every task report must contain.

### Re-ordered 2026-09-15 after the first-pass review

36 tasks completed, **goal metric unmoved** (TPC-H 6/22, TPC-DS 2/99) and
TPC-DS categories net worse (525 -> 540). The review
(`tmp/METHODLOGY3_RALPH_CHECK0915/`) found one dominant reason and it decides
this ordering: **narrowing runs after costing, so M0139's landed work cannot
move a single plan.** Unblocking that comes before starting anything new.

Select in this order:

1. **M0141-S2a-fix and M0139-0007 — the costing-order unblock. TOP PRIORITY.**
   These are the same defect seen from two sides: narrowing/width information
   reaches the cost model too late (or in the wrong currency) to affect plan
   selection. Until one of them lands, **M0139's entire +671 lines contribute
   nothing to the metric and M0141's remaining slices cannot be judged.**
   Take `M0139-0007` first (it establishes the absorption principle the
   S2a-fix then applies), unless its scoping recon says otherwise.
   **RESOLVED 2026-09-15: both halves are now landed-and-decided.**
   M0141-S2a-fix1 landed (TPC-H `match` 6→8); fix2 was attempted and declined
   (measured net-neutral/negative, reverted). M0139-0007's three filed pieces
   — the recon, 0007a (R108/R113 measurement, both HOLD), 0007b (Memoize
   absorption, HOLD) — are all complete; only the narrow M0139-0007c follow-up
   (Memoize's un-absorbed per-key width term, not on the critical path) is
   still open. **The next loop should select item 2 below (M0137's re-opened
   0014–0017)** unless a later banner edit says otherwise.
2. **M0137's re-opened tasks (0014–0017).** Instrument and orphan-mechanism
   debt found by the review. 0015 is one line and 0016's gate is already
   satisfied. **0017 matters more than its size suggests**: TPC-H plans are
   captured `-serial` only, so `parallelism` is measured *out* of the headline
   6/22 and one of the nine categories is currently unscoreable.
   **RESOLVED 2026-09-15: item 2 is fully closed (0014, 0015, 0016, 0017 all
   `[x]`).** 0017 landed the parallel-mode PG baseline and measured
   `parallelism=16/22`, `match=2/22` at `-serial=false` — see
   `docs/design/0100-0149/m0137-0017-serial-and-parallel-capture.md`. Its own
   follow-up (triage the 16 divergences) is filed separately as **M0137-0019**
   (listed under the M0137 milestone section below, not in this fixed
   0014-0017 set) — it is new investigative work sized like M0141/M0142's
   remaining slices, not a re-opened harness debt, so it does not inherit
   item 2's priority; the next loop applies normal banner order to it.
   **The next loop should select item 3 below** (M0138 0007-0009 /
   M0140-0006) unless a later banner edit says otherwise.
3. **M0138 — PG-faithful ANALYZE statistics** (0007–0009: the orphaned
   `numeric` `avg_width`, the category-shift bisect, the correlation banding
   re-open) and **M0140 — TPC-DS parallelism** (0006: the partial-Append
   producer that M0140-0004 deferred without an owner).
   **RESOLVED 2026-09-15: item 3 is fully closed.** M0138 0007-0009 were
   already `[x]` before this loop. M0140-0006 is closed as a decomposition
   (design doc `docs/design/0100-0149/m0140-0006-decomposition-into-a-b-c.md`):
   the recon's own sizing (three pieces, each a whole C-19-series slice) made
   blind implementation a one-task-per-loop violation, so it is split into
   **M0140-0006a/0006b/0006c** (filed under the M0140 milestone section
   below) — none selected yet. **The next loop should select item 4 below**
   (M0141's remaining slices / M0142) unless a later banner edit says
   otherwise, or pick up M0140-0006a as the first of the new sub-tasks if it
   judges that a better use of the banner's ordering (item 4's M0142-0004 is
   itself just a re-measurement, so either is a reasonable next pick).
4. **M0141's remaining slices** (S2b, S3–S6, and **S7 — Incremental Sort**,
   which 14 TPC-DS queries need before they can match at all) and **M0142 —
   Join-order costing** (0003c onward, plus 0004–0008 promoted from the ledger).
   Take **M0142-0004** first inside that milestone: it is a re-measurement, and
   until it runs, nothing is known about how large TPC-DS's row-estimate error
   still is — all four cuts the ledger named have since landed and the "3–5
   orders out" figure predates every one of them.
5. **M0143 — Engine correctness carry-overs.** Gated on nothing; select it
   whenever everything above is blocked. **Still 7/7 untouched.**
6. Then M-NIGHTLY's own open items, then the pre-existing milestones
   (M0119 -> M0122 -> M0131 -> M0134 -> M0135/M0136 -> M0095/M0110).

**Three owner decisions of 2026-09-15 bind every M0137–M0143 task below** (full text in
`AGENT.md` §"Further owner decisions"): **B1** `minimize_datum`/packed retention
is **NO-GO** and out of scope — do not re-propose it; **B2** the direction (same
statistics, same plan) is unchanged, but irreducible goopg/PG representation
differences must be **absorbed** by giving the cost model the PG-equivalent
quantity rather than goopg's native one — this is not tuning, and it is the
route that replaces packed retention; **B3** `GOOPG_GATHER_PATHS=all` stays
default-on and may not be reverted on category count alone.

**M-NIGHTLY: filing stays unconditional, selection does not.** Every loop still
reads `ci/logs/action-items.md` and files each new `## AI-` subject under the
M-NIGHTLY milestone below — that obligation is unchanged and outranks
everything, because it costs minutes and preserves nightly-regression
visibility. **But M-NIGHTLY items are no longer selected ahead of M0137–M0143.**
Two carve-outs still preempt: an item that breaks the build, and an item that
breaks a gate the plan-parity group depends on.

**M-NIGHTLY selection rule (applies when an M-NIGHTLY item is selected, per
ci/design/07-ralph-feedback.md §B):**
1. Before investigating, re-run the item's repro at HEAD — the log reflects the
   last nightly run and may be stale.
2. Fix with the normal gates (practice cards apply), cite the AI-id in the
   commit message, check the task off.
3. The next nightly run confirms and drops the item from the log.
