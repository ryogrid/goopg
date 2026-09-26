Task: M0146-0002i — parameterized-probe partial NL under SEMI (TPC-H Q4). COMPLETE, gates all PASS, committing this loop.

Files:
- internal/optimizer/gatherpaths.go — new shared `partialProbeNestLoopJointype` {INNER,SEMI}; probe arm of `partialPathDrivingKind` reads it.
- internal/optimizer/parallel.go — new `partialProbeNestLoopJoinType` (optimizer domain twin); `lateralProbeJoinIsPartialCapable` reads it.
- internal/executor/parallel_scan.go — `lateralProbeJoinPartial` jointype set {INNER,SEMI}.
- Tests re-pinned to {I,S}: partial_nestloop_test.go (new TestPartialPathDrivingKindNestLoopProbe), partial_lateral_test.go, partial_nli_memoize_test.go, executor/parallel_lateral_probe_test.go.
- NEW executor/parallel_lateral_semi_probe_identity_test.go — row identity `-race` workers 1/2/4; uses `optimizer.SetIndexProbeCostMultiplier("1")` to elect plain index probe over bitmap.
- analysis/m0146/m0146-0002i/ (evidence), design doc 0002a §"M0146-0002i outcome", fix_plan (boxed), deferral_ledger row.

Key symbols: partialProbeNestLoopJointype / partialProbeNestLoopJoinType / lateralProbeJoinPartial / partialPathDrivingKind / addPartialNestLoopPaths (producer unchanged {I,S}).

Hypothesis/Findings: confirmed — the filed-but-undrivable SEMI probe starved PartialPathlist[0]. After widening, Q4 = PG spine (NL Semi inside Gather, per-worker idx probe; 76255 vs PG 70092; join-order 14→13, agg-strategy 6→5; residual sort-strategy/parallelism = Gather Merge + Partial GroupAggregate upper, owned by M0146-0003/0025). TPC-DS moved zero plans (fireset fires=none both scales); acceptance arm 24/24 value-identical.

Next step: M0146-0002j — same three gates get JoinAnti PLUS producer gate joinpathsnli.go:465. Q21 is the consumer (NL Anti inside Gather probing l3 idx per worker).

Gates run: units PASS; tpch-spotcheck PASS (Q12=2,Q13=33); tpcds-sf025 PASS=96/0/0 plans 99/99; tpch-acceptance-arm PASS 24/24; tpcds-fireset PASS (no fires); race pin PASS; canonical tpch capture measured (match=6, Q4 categories 4→2).

In-flight: none.
