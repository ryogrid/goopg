# M0127-P6.1 — fusion removal inventory (captured 2026-08-07, at `bedd50fd`)

Read-only grep sweep taken while nightly `20260807-004620` was running, so the
S7 deletion loop does not have to re-derive it.

## Delete outright

| path | what |
|---|---|
| `internal/executor/fused_hash_join.go` | whole file (17,976 B): `fusedLevel` (14 fields), `fusedHashJoinOp` (9 fields), `tryFuseHashCascade`, `Open`/`Next`/`Close`/`Schema` |
| `internal/executor/fused_hash_join_test.go` | whole file — every test is a fusion predicate/field-count test |

## Hook sites (2, both `internal/executor/executor.go`)

- `:171` — `case *planner.Join:` in `Build`, `if fused, ok := tryFuseHashCascade(env, p); ok`
- `:570` — the second builder path, `tryFuseHashCascade(tree.env, p)`

Both fall through to the ordinary cascade when fusion declines, so removal is a
straight deletion of the `if` block, not a re-plumb.

## Env / config

- `GOOPG_RUNTIME_JOIN_FUSION` (kill switch, **defaults OFF**) — `fused_hash_join.go:54`
- `GOOPG_RUNTIME_JOIN_FUSION_MIN_LEVELS` — `fused_hash_join.go:61`
- `executor.go:26` / `:34` comments describe `inWorker=true` so fusion declines
  in `BuildWorker`, and the `env` struct carrying "root + inWorker + fusion
  config" — the fusion fields on `env` go too; `inWorker` has other readers.

## Orphan export to re-check after deletion

`planner.IsCanonicalKeyEquality` (`internal/planner/bushy.go:1751`, exported
wrapper). Its only non-comment callers are `fused_hash_join.go:474` and `:604`;
`join_hash_keys.go:187` and `join_hash_keys_test.go:88` merely name it in
comments. So it becomes a true orphan export at P6.1 — unexport or delete it
(the file it lives in, `bushy.go`, dies at P6.3 anyway).

## Closes by construction

`internal/executor/operators_lockrows.go: markJoinPreserveCTID` has no
`fusedHashJoinOp` arm, so a `FOR UPDATE` over a fused join silently took no row
lock. Deferral rows `2026-08-06 M-NIGHTLY (root-0038)` and
`2026-08-06 M-NIGHTLY (AI-20260806-011323-001)` both name P6.1 as the closure
event for the fused half (P6.2 for `multiHashJoinOp`). Cite them when P6.1
lands rather than adding a walker arm to code being deleted.

## Bar (fix_plan)

grep-clean + UNITS + SPOT. Do **not** run SPOT while a nightly's tpch/tpcds
stages are live — it perturbs their timings.
