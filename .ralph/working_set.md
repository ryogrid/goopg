Task: M0142-0003f — add the missing TPC-H FK constraints to goopg's `:65433`
bench cluster to restore parity with the PG oracle, then re-measure Q9.
**DONE and committed this loop, PARTIAL: 11/16 landed, Q9 re-measurement
still blocked.** Two follow-ups filed: M0142-0003g (the real blocker) and
M0142-0003h (secondary, untraced).

Files: `.ralph/fix_plan.md` (M0142-0003f rewritten `[x]` PARTIAL; new
M0142-0003g/0003h filed `[ ]`). `.ralph/deferral_ledger.md` (3 new rows for
m0142-0003f). `docs/design/0100-0149/m0142-0003f-fk-add-blocked-by-unindexed-validation-scan.md`
(new). `docs/design/README.md` (indexed). No `internal/` files touched —
this was live DDL against the shared bench cluster + a read-only recon
subagent trace, not production code.

Key symbols (read, not edited): `internal/executor/operators_ddl.go:9033`
(`AlterTableAddForeignKey` dispatch), `:12089`/`:12207`
(`execAlterTableAddPrimaryKey`/`...UsingIndex`), `:13974`/`:13995`
(`validateFKConstraintExistingRows(Rel)`); `internal/executor/operators_fk.go:632`
(`assertParentExists`), `:1318` (`scanRelForFKMatch`).

Findings this loop: (1) Adopted goopg's 8 pre-existing unique indexes as
`PRIMARY KEY`s via `ADD CONSTRAINT ... PRIMARY KEY USING INDEX` — cheap,
matches PG's constraint names exactly. (2) Added the 3 FKs with tiny parent
tables (`nation_region_fk`/`customer_nation_fk`/`supplier_nation_fk`) —
landed, PG-matching, **permanent on the live shared cluster**. (3) A
`BEGIN;...ROLLBACK;` dry run of all 16 target DDLs, meant to be
non-destructive, instead hung past a 900s timeout on the 12th statement
(`partsupp_part_fk`, child `partsupp` 800k / parent `part` 200k); killing
the client left the first 11 statements **already committed** — the
intended `ROLLBACK` was never reached (see M0142-0003h). (4) **Decisive
root cause** (traced by a read-only recon subagent): goopg's FK validation
is an O(child rows × parent heap-scan distance) unindexed nested-loop —
`assertParentExists`/`scanRelForFKMatch` never consult the parent's
existing unique B-tree, only cheap when the parent table is tiny. Confirmed
infeasible at `partsupp`/`part` scale, let alone `lineitem`'s 6M rows. (5)
The scan also has **no interrupt-check**: `pg_terminate_backend` on the
stuck backend (PID 81) returned success but it kept running 15+ seconds
later with no sign of stopping — contrast PG's own
`validateForeignKeyConstraint` (`tablecmds.c:13694`), O(child rows) via a
planner `LEFT JOIN` or an indexed RI-trigger probe, with per-row
`CHECK_FOR_INTERRUPTS()`. Both gaps folded into one follow-up,
**M0142-0003g**, since the same function needs touching for either fix.

Next step: **M0142-0003g** — index-accelerate `assertParentExists`/
`scanRelForFKMatch` (probe the parent's existing unique index instead of a
full heap scan) and add a cancellation check in the per-row loop. Once it
lands, resume -0003f: add the remaining 5 FKs (`partsupp_part_fk`,
`partsupp_supplier_fk`, `order_customer_fk`, `lineitem_partsupp_fk`,
`lineitem_order_fk`), land the DDL in `bench/tpch/build_schema_goopg.sh` (or
a new post-load step) so it survives `--reset`, then re-run the Q9
`EXPLAIN`/estimate-audit from -0003a/-0003d — `lineitem_partsupp_fk`
specifically is what Q9's collapse needs. **M0142-0003h** (smaller, separate
recon: does DDL under an explicit `BEGIN` actually wait for `COMMIT`/
`ROLLBACK` on this cluster, or force-commit per-statement?) can be picked up
independently. Neither is mandated over other M0142/M0141 items by the
banner (still item 4) — other open items unchanged from last loop's note:
M0142-0005 (per-worker Memoize cache recon), M0142-0008a/0008b (SEMI/ANTI
decorrelation scoping, filed 2026-09-16), M0142-0016c (Q33/Q54/Q56 shape
check), M0141-S2b/S3-S7 (upper-planner ordering, Incremental Sort).

Gates run: `git status --porcelain -- internal/` empty before AND after
(no production code touched). `make ralph-state-guard`: same pre-existing
stale progress-marker pattern as recent loops, self-repaired, passed clean.
Pre-commit pgbench smoke gate: ran at commit time, **PASS** (43/43/145 TPS
across TPC-B/simple-update/select-only, 0 failed). Practice-card row-count
gate suite not required (no production code touched).

In-flight: **backend PID 81 on the shared `:65433` bench cluster is still
running** `ALTER TABLE partsupp ADD CONSTRAINT partsupp_part_fk FOREIGN KEY
(ps_partkey) REFERENCES part(p_partkey);` — started ~06:0x this loop, does
NOT respond to `pg_terminate_backend`, does NOT block ordinary reads on
`partsupp`/`lineitem` (verified), left running deliberately rather than
force a disruptive server restart (see design doc "Disposition"). **The next
loop should re-check `SELECT count(*) FROM pg_constraint;` (11 as of this
loop's end) and `SELECT pid,state FROM pg_stat_activity WHERE pid<>
pg_backend_pid();` before assuming cluster state is unchanged** — if
`partsupp_part_fk` finished on its own, that's a 12th constraint landed for
free; if the process is gone entirely, the server may have restarted
(another loop's action) and the backend info is stale. No other process was
left running by this loop.
