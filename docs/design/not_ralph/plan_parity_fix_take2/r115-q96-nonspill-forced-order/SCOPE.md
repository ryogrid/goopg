# R115 SCOPE — Q96 non-spill forced-order cost attribution

## 1. Question and dependency

R111 established a valid, type-aware common-input Q96 oracle. On that oracle,
the two immutable forced forms both return 266 and have repeatable plans, but
their cost ordering disagrees:

| forced first-two-dimension order | PG18.3 total | Goopg total |
|---|---:|---:|
| `store_sales -> household_demographics -> store` (hdem-first) | 17669.88 | 27661.94 |
| `store_sales -> store -> household_demographics` (store-first) | 17845.79 | 27654.26 |

Thus PG prefers hdem-first by 175.91 while Goopg prefers store-first by 7.68.
This remains after R88--R91 unique/inner-unique work. R108 then tested the
separate PG packed-HashJoinTuple spill-I/O representation: the Q96 **selected**
hash candidates had one PG batch, so its switch was inert for those selections
and cannot explain this non-spill margin. R115's census must not generalize
that result to unobserved offered candidates.

R115 resumes the Q96 breakdown. It is a measurement-only scope: identify the
first cost input or term that is both (a) different enough to account for the
forced-order margin and (b) representable from lawful Goopg/PG evidence. It
does not authorize a cost change, a Datum-size change, a statistics change, a
path admission change, or forcing the natural Q96 relation order.

## 2. Why previous evidence is insufficient

R98 reconciled Goopg's then-filed inner-unique inputs but PG did not expose
the loser-side semifactors or virtual geometry. R101 exposed PG's forced loser
price; R105 exposed Goopg's forced price; R106 could only trace Goopg upper
paths and found materially different base estimates. R111 repairs the missing
common-input premise, but deliberately records rather than equates the two
engines' native ANALYZE statistics. A difference now can be one of:

1. a Goopg calculation/transcription defect with inputs both engines expose;
2. a genuine engine-native statistics/selectivity difference;
3. a PG cost input that is not observable or not represented by Goopg; or
4. an instrumentation/provenance failure.

Only outcome 1 can lead to a later implementation scope. Outcomes 2--4 are
findings, not permission to tune a coefficient to make Q96 choose a target
order.

## 3. Fixed forms and isolation

Use exactly the immutable R101 forms, verified by the R111 SHA-256 values:
`hdem-first` `d83c3d58d7d4b543d68908da78de79bad900eb230e0dbe830faf27eec6293784`
and `store-first`
`cda057e138b1fd9f9731b7249ea893a257f5c1c9561cffea2b5b8182fc27d5d6`.

All Goopg measurements use a private disposable copy/rebuild of the validated
R111 data, not shared benchmark ports or data directories. PG uses the named
private native-PG18.3 R111 oracle. Pin `work_mem=64MB`,
`max_parallel_workers_per_gather=4`, `parallel_leader_participation=on`, both
collapse limits to one, and the R111 Goopg partial/gather controls. Verify
binary identity and query hashes before capture. The forced syntax is a probe
only: it never establishes what the natural dynamic-programming election
should choose absent an independently explained cost difference.

## 4. Temporary diagnostic contract

After this scope is reviewed, committed with `-n`, and pushed, a temporary
default-off `DPQ96COST` diagnostic may be added. It must be enabled only by an
exact environment value and must not alter planner state, cost, cardinality, or
path decisions. A Hash Join cost call occurs before `addPath` and before a
winner is selected, so it must emit **every** invocation, not claim to know a
future selected result. It must emit separate `DPPATH`-style records at the
defined path-filed and final-selected loci.

The streams must join through a collision-free trace identity:
`form/run + planner invocation + producer/call-site + parent relids + ordered
outer/inner relids + join kind/build side + parameterization + candidate
occurrence`. The occurrence is a trace-local counter scoped to one planning
run; its sidecar is observational only, discarded at the end of that run, and
never read by optimizer code. A focused test must show duplicate structural
candidates get distinct identities, every filed record maps to exactly one
prior cost record, and every final selected path maps to its filed record.
Root totals must be reconciled from mapped selected records rather than
inferred from a cost-call record alone.

Each pre-filing cost record has a form/run delimiter and includes:

- its trace identity, join level, relation-id set, join type and build side,
  serial/partial coordinate, workers/divisor, and explicit `phase=cost`;
- outer/inner rows, startup/total costs, output rows and width, input column
  count/average variable bytes, base restriction/filter provenance, hash
  clause count, parameterization, and all known selectivity inputs;
- common initial-hash terms and every final-cost term separately: probe hash,
  bucket walk, output CPU, residual quals, inner-unique proof/match fraction,
  virtual bucket count, and disabled-node contribution;
- both Goopg and independent PG-formula values for any term whose PG inputs
  are actually available, plus an explicit `unknown` reason otherwise; and
- planner/executor representation geometry separately: Goopg map sizing,
  PG packed hash-tuple batch sizing, PG relation-byte spill pages, and elected
  currency. For this scope, one-batch must be recorded as a finding, never
  silently omitted.

The filed/selected records add the same identity, `phase=filed` or
`phase=selected`, and the elected path total; they may not recompute or alter
the path. The diagnostic is not allowed to read live PG state, invent semifactors, infer
MCV frequency from an executor map, alter cost/cardinality/path fields, or
change a plan. It must be removed before the R115 report is staged. The final
source diff must be empty except for documentation.

## 5. Required measurements and falsifiers

1. Capture each Goopg forced form twice with the diagnostic off, then twice on;
   after stripping only volatile costs, rows, widths, timestamps, paths, and
   run labels, each corresponding plan must be byte-identical. Each execution
   must return 266. A trace-induced shape or value change invalidates the
   diagnostic and stops attribution.
2. Capture each form on the private PG oracle as text and JSON `EXPLAIN`, plus
   `EXPLAIN (ANALYZE, BUFFERS)` only when it can run without changing data.
   Record planner estimates separately from observed spill/batch evidence.
3. Reconcile the two Goopg form totals from the diagnostic; unaccounted cost
   is an instrumentation failure, not a theory. Then compare their term delta
   against the PG 175.91 direction/magnitude using only PG-visible source or
   plan inputs.
4. Independently record the relevant native statistics, including null
   fractions, MCV/histogram availability, `n_distinct`, correlation, relation
   rows/pages, and index uniqueness for every Q96 predicate/join column. Do
   not call unequal native statistics a Goopg costing bug without showing the
   exact consumer and its impact.
5. Run focused tests for the temporary trace, then foreground
   `go test ./internal/optimizer`, `go test ./internal/executor`,
   `go vet ./internal/optimizer`, and `git diff --check`. The post-revert
   diagnostic-free A/A control and the retained Q96 values are mandatory.

## 6. Decision table and stop rules

| result | permitted next step |
|---|---|
| A concrete Goopg term has a wrong transcription and all its PG inputs are evidenced | scope that one term separately; do not implement in R115. |
| The delta follows a known, differing native statistic | file an input/statistics investigation; no cost compensation. |
| The decisive PG input is unobservable or unrepresented | record the boundary and choose another Q96-visible discriminator; no guessed constant. |
| The trace changes plan/value or cannot reconcile filed costs | remove it, report instrumentation failure, and do not attribute. |
| A non-spill representation coordinate is reached but independently inert | record the negative result and move to the next Q96 discriminator; do not reopen Datum-size work. |

R115 explicitly excludes HashAggregate/Datum-size work (R114 is deferred by
user direction), Hash Join spill-price promotion, Gather forcing, modifying
ANALYZE results, SQL rewrites, and broad planner tuning. A final report must
state which table row occurred, retain commands/artifact paths, and update
TODO before a successor scope is made.
