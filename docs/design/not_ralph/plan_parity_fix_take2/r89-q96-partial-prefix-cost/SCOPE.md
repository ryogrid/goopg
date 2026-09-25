# R89 SCOPE — attribute Q96's post-R88 partial-prefix cost election

R89 follows R88 (`dae2ebeb7`) and is measurement-only. It does not authorize a
cost-model change, a planner preference, a GUC/default change, a Gather/NLI
change, or any forced Q96 tree.

## 0. Evidence and selected boundary

R88 removed the bare-unique/FK selectivity conflation. The remaining natural
Q96 difference is decided earlier than Gather: in the same partial relset,
Goopg retains `(ss ⋈ store) ⋈ hd` over the PG-shaped `(ss ⋈ hd) ⋈ store`.

Under the pinned SF0.25 session (`work_mem=64MB`, four maximum workers,
leader participation), R88's diagnostic `GOOPG_GATHER_PATHS=top` trace records:

| candidate | rows | startup | total | input total | path properties |
| --- | ---: | ---: | ---: | ---: | --- |
| L2 `ss ⋈ hd` | 22,186 | 149.00 | 16,497.86 | 15,256.18 | partial hash; zero pathkeys/disabled |
| L2 `ss ⋈ store` | 18,491 | 1.16 | 16,313.07 | 15,256.18 | partial hash; zero pathkeys/disabled |
| L3 `(ss ⋈ hd) ⋈ store` | 1,767 | 150.16 | 16,599.89 | 16,497.86 | partial hash; zero pathkeys/disabled |
| L3 `(ss ⋈ store) ⋈ hd` | 1,767 | 150.16 | 16,549.08 | 16,313.07 | partial hash; zero pathkeys/disabled |

Thus the non-PG L2 candidate leads by 184.79 and its L3 continuation by
50.81. The latter is the input to the later partial NLI; any Gather/serial
issue is downstream and not selected in this round.

PG source is read-only oracle material. The relevant contract is
`initial_cost_hashjoin` and `final_cost_hashjoin` in
`postgres/src/backend/optimizer/path/costsize.c`: build input and hash-table
work are startup, while bucket/qual walks and output tuple/tlist work are run
cost. R89 must determine whether Goopg's corresponding partial-path terms
(`internal/optimizer/joinpathsparallel.go` and `cost_funcs.go`) differ in an
input, a source-faithful formula, or merely an intentionally different model.

Not selected:

* revisiting R88's ordinary equality treatment, null factors, FK logic, or
  partial-index policy;
* changing statistics/data, query text, work_mem, worker count, or an enable
  GUC;
* implementing parallel execution, Gather admission, NLI execution, or a
  Q96-specific tie-break;
* inferring a fix from final totals without an attributable source term.

## 1. Measurement design

On a fresh private clone and unused 55xx port, build from the committed R89
scope and run all programs foreground. Retain artifacts under `/tmp/pp2/r89/`.
Use the exact R88 session GUCs and live PG 18.3 at `:65438/tpcds025`.

1. Capture natural Q96 trace A/A and a `GOOPG_GATHER_PATHS=top` diagnostic
   trace. Confirm that diagnostic mode does not change the selected natural
   tree.
2. Capture PG's chosen partial prefix with the same GUCs and collect every
   input needed by the source formulas. First establish `parallel_hash` for
   each path. Record its per-worker partial outer rows separately from its
   complete, undivided inner/build rows when `parallel_hash=false`; do not
   relabel the latter as per-worker. Record raw/build rows and widths, hash
   clauses, bucket/batch/spill state, virtual bucket count, inner bucket
   fraction and MCV frequency, `Inner Unique`, `outer_match_frac`,
   `match_count`, rounded `outer_matched_rows`, and `inner_scan_frac`.
   Also record hash-qual, qpqual (after any source subtraction), and
   pathtarget startup/per-tuple costs, output rows, and all applicable cost
   GUCs (including CPU, I/O, memory, and parallel settings). This is required
   to exercise PG's separate matched/unmatched bucket charges rather than
   approximating them from total rows.
3. For each four rows of the table above, decompose Goopg startup and run cost
   into inherited outer run, inner/build total, hash-build CPU, hash/bucket
   qualification CPU, output tuple/restrict/tlist CPU, spill/batch I/O, and
   any parallel adjustment. Present exact values and equations, not residual
   prose.
4. Evaluate PG's `initial_cost_hashjoin` and `final_cost_hashjoin` equations
   in two explicitly separate coordinate spaces: first, using the exact
   Goopg candidate-path inputs for all four table rows (the same-coordinate
   counterfactual); second, using live-PG inputs where those inputs are
   observable and reproducible. Name a rejected PG candidate whose state is
   unavailable as unavailable; never infer or force its inputs. State
   explicitly which source terms Goopg cannot represent, and do not fill a
   gap with a guessed constant.
5. Select exactly one outcome using this precedence, so attribution remains
   exhaustive:
   * **C4**: necessary evidence cannot be recovered even with default-off
     instrumentation; report the missing evidence and stop.
   * **C2**: evidence is available, but the applicable PG source branch
     requires an input or term Goopg cannot represent; name the branch and
     missing representation.
   * **C1**: inputs and branch are representable, but a Goopg term differs
     from the PG-source calculation on the same Goopg inputs; classify the
     difference as a defect or a documented deliberate model difference.
   * **C3**: Goopg is source-faithful at this boundary on the same inputs;
     identify the first differing upstream input (or the next boundary) for a
     later scope.

Temporary trace code, if required, must be default-off, must print enough
inputs to recompute the equation, and must be removed before the result commit.

## 2. Mandatory controls

Before reporting any outcome:

1. run focused optimizer tests, `go test ./internal/optimizer
   ./internal/executor ./internal/testutil/estimateaudit`, and the matching
   `go vet` command;
2. run Q96 value comparison against PG and Q9/Q41/Q91/Q96 trace-on/off byte
   controls; and
3. leave the full TPC-H digest, SF0.25 values sweep, and live-PG census for a
   later implementation round. This round changes no production behavior, so
   it must not spend those value gates merely to re-prove an unchanged binary.

## 3. Acceptance and process

No production code is authorized by this scope. The scope requires agent
review, correction, `git commit -n`, and push before R89 measurement begins.
The measurement report likewise requires review and a separate artifact
commit/push. Only C1 or C2 may lead to a later, separately reviewed
implementation scope.
