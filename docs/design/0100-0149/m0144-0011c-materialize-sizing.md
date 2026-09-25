# M0144-0011c (recon) — sizing `Materialize`, and why the root escalates here

Status: RECON COMPLETE 2026-09-20 — root M0144-0011 escalated under AGENT.md S4
Kind: recon
Parent: M0144-0011
Milestone: M0144 (plan-parity harness, measurement-first era)

## 1. What was asked, and what the sizing found

M0144-0011c was filed as "`Materialize` node existence (path producer +
executor node + EXPLAIN rendering), or measured residue". The sizing changes
the picture in one important way:

**The executor half already exists and already runs.**
`internal/executor/operators_material.go` (308 lines) is a faithful
`nodeMaterial.c` analogue — lazy fill, `eof_underlying` semantics, a
`work_mem`-bounded resident prefix with a sequential spill file — and
`internal/executor/join_nl_stream.go:108` wraps **every** streaming nested-loop
inner in it:

```go
inner := newMaterializeOp(o.right)
inner.openCached(ctx)
if !nlInnerWorkMemEnabled {
        inner.setUnbounded()
}
```

So goopg does not lack materialization. It lacks three things PG has:

1. a **plan node** the planner places and EXPLAIN prints;
2. a **cost** for it (`cost_material`) and for the rescan it enables
   (`cost_rescan`);
3. PG's **admission rule**, i.e. materializing the cheapest inner *only* when
   that inner does not already materialize its own output.

`operators_material.go`'s own header states 1-3 as deferred, and
`join_nl_stream.go`'s comment already records the consequence of 2: the inner
cache runs UNBOUNDED by default because "`cost_rescan` prices exactly this case
and the planner picks another path; `costInnerNestLoop` has no such term yet".
Measured there: TPC-DS SF0.5 Q54 went 144 s → >400 s with the bound on.

## 2. The movement this would buy

`pg-plan-parity-diff.py` on the post-M0144-0011b-1 SF0.25 capture:
`missingnode=25`, of which

- **13 records cite `Materialize`** — Q1, Q8, Q10, Q16, Q35, Q47, Q57, Q58,
  Q59, Q65, Q77, Q78, Q91;
- 14 cite `Incremental Sort`;
- Q35 and Q58 cite BOTH, so they would stay `MISSING-NODE` on the Incremental
  Sort ground alone.

So a faithful `Materialize` is worth **`missingnode` 25 → 14 (−11)** — far
outside the ±3 noise band, and the only remaining hypothesis in this lineage
with a movement claim of that size.

(`Incremental Sort` is a different matter and NOT a missing node: goopg has
`*IncrementalSort` and `addIncrementalSortPaths`, gated off by default because
the candidate loses on cost. That is banner item 5, M0144-S7 / M0141-S7, and is
explicitly cost-diagnosis-only.)

## 3. Why it is not implemented in this loop

It is a multi-slice build, and the part that is missing is the expensive part.

Adding an optimizer plan node in this codebase is not a one-file change: a new
node type has to be threaded through `createPlan`, the EXPLAIN renderer, a new
`PathKind`, the cost model, the NL path producer and its partial twin, and the
several exhaustive node-kind switches that already exist (`legacyDisplayChildren`,
`planChildren`, the `lockRowsOp` walkers, the narrowing passes). A half-threaded
node does not compile, let alone pass a gate, so there is no honest partial
landing.

The PG surface to port is, by contrast, small and completely specified:

| piece | PG source |
|---|---|
| admission | `postgres/src/backend/optimizer/path/joinpath.c:1890-1901` — materialize `inner_cheapest_total` unless `enable_material` is off or `ExecMaterializesOutput(pathtype)` |
| the exclusion set | `postgres/src/backend/executor/execAmi.c:640-647` — `T_Material`, `T_FunctionScan`, `T_TableFuncScan`, `T_CteScan`, `T_NamedTuplestoreScan`, `T_WorkTableScan`, `T_Sort` |
| path | `create_material_path`, `postgres/src/backend/optimizer/util/pathnode.c:1637` |
| cost | `cost_material`, `costsize.c` — `run_cost += 2 * cpu_operator_cost * tuples`, plus `seq_page_cost * ceil(nbytes/BLCKSZ)` when `relation_byte_size(tuples,width) > work_mem` |
| rescan | `cost_rescan`'s Material arm, `costsize.c` — `cpu_operator_cost` per tuple |

### A finding the implementer must not miss

goopg's executor materializes **every** NL inner; PG materializes only the ones
its admission rule admits. So "emit a `Materialize` node wherever the executor
materializes" would produce **goopg-only** `Materialize` nodes in exactly the
cases PG excludes — trading 11 `missingnode` records for a new mismatch class.
The node's placement must follow PG's rule, and the executor's unconditional
wrap must then be made conditional on the node's presence, or the plan and the
executor will disagree about what the plan says.

That coupling — a planner change that requires a matching executor change, on a
path whose unbounded cache is a known >2.5x cliff (§1) — is why this needs its
own design doc, its own slices and its own gate budget rather than the tail of
an already-long lineage.

## 4. Proposed slices (for the owner, per S4)

1. **`Materialize` plan node + EXPLAIN + `createPlan`**, emitted by nothing
   yet. Gate: the corpus is byte-identical (a node no producer files is inert,
   and C6 requires the trace count of zero as evidence).
2. **`cost_material` + `cost_rescan`'s Material arm** as pure functions with
   unit tests against the PG expressions. Still no producer. Inert by the same
   argument.
3. **The NL admission rule** (`joinpath.c:1890-1901` + `ExecMaterializesOutput`'s
   exclusion set) files the matpath candidate, and `join_nl_stream.go` stops
   wrapping unconditionally. This is the slice that moves plans and needs the
   full gate battery. Expected movement: `missingnode` 25 → 14 (−11) on the 11
   queries that cite `Materialize` alone.
4. **Re-time Q54-class plans** — the `nlInnerWorkMemEnabled` cliff becomes
   priceable once slice 2 lands, so the `GOOPG_NL_MATERIALIZE_WORK_MEM` default
   can be revisited with a number instead of a caution.

## 5. Lineage

With this recon, root **M0144-0011** has five consecutive completed descendants
reporting `Movement: none`:

| # | descendant | Movement |
|---|---|---|
| — | M0144-0011a | yes — SF0.25 `sort-strategy` 67 → 60 |
| 1 | M0144-0011a-3 | none |
| 2 | M0144-0011a-2 | none |
| 3 | M0144-0011b (recon) | none |
| 4 | M0144-0011b-1 | none |
| 5 | M0144-0011c (this) | none |

AGENT.md S4 is therefore met: no further descendant is selected or filed, an
escalation block goes into the root, the root is marked `[!]`, and the loop
selects elsewhere. The escalation block carries §2's number and §4's slices so
the owner can reopen with the size in hand.

Note for the owner: the lineage is not unproductive in substance — it removed a
redundant Sort corpus-wide (0011a, the one `Movement: yes`), repaired two false
premises by measurement, and fixed a 650x cost-monotonicity violation that
flipped Q8's join method to PG's (0011b-1). What it has not done since 0011a is
move a category past ±3, which is the only thing S4 counts.
