(idle — nothing in flight)

M-NIGHTLY (tpch/Q15b-MAIN-explain, AI-20260707-000712-006) FIXED and checked
off in fix_plan.md this loop. Task fully resolved — no follow-up required
unless a future regression reopens it.

Root cause was in the benchmark tool itself (`cmd/tpch-runner/main.go`), not
the goopg engine: `runQ15WithCancel`/`runQ15` (HammerDB Q15's CREATE
VIEW/SELECT/DROP VIEW three-statement special case) guarded the
`Q15-CREATEVIEW` and final `drop view` steps behind `if !doExplain` —
intended only to avoid wrapping DDL in `EXPLAIN`, but this also skipped
*running* CREATE VIEW as a real statement under `-explain`, so `revenue0`
never existed when `EXPLAIN <Q15MainSelect>` tried to resolve it (`pq:
relation "revenue0" does not exist (42P01)`). Fixed by making CREATE VIEW /
DROP VIEW run unconditionally in both functions; `Q15a-VIEWBODY`/`Q15b-MAIN`
still honor `doExplain` as before.

Verification this loop: built `tmp/goopg-bench-bin` + `tmp/tpch-runner` at
HEAD, started a fresh server on the canonical
`bench/tpch/runtime_goopg/data` (port 65433, via `scripts/goopg-test-run.sh`
with `--hba` — mirrors `scripts/tpch-spotcheck.sh`'s own recipe; a first
manual attempt without `--hba`/the wrapper script hung indefinitely on
startup, so always reuse the script's exact invocation). Full 22-query
`-explain` sweep completed with zero errors (previously failed at
Q15b-MAIN); `-queries 15` without `-explain` still gives the correct real
result (Q15a rows=10000, Q15b rows=1); Q12/Q13 spot-check run manually
against the same server: Q12=2, Q13=33 (full parity, matches
`bench/tpch/spotcheck_expected.env`). `go build ./...` clean. No new
automated regression test added (documented rationale in fix_plan.md: the
function drives real `*sql.DB` calls with no unit-test seam; this loop's
manual run reproduced the exact nightly failure command byte-for-byte, which
is the authoritative repro per the M-NIGHTLY rules). Server stopped cleanly
via `goopg stop -D` (systemd scope self-removed on process exit, verified via
`ps aux` — no leftover process).

Not yet committed — next action is `git add`/`git commit` for this fix
(cmd/tpch-runner/main.go + .ralph/fix_plan.md), citing AI-20260707-000712-006,
then `make ralph-state-guard` before the status block.

Next step after committing: pick up `tpch/Q9-timeout`
(AI-20260707-000712-007) — Q9 hit its per-query budget (57014/cancel) —
then `tpch/Q20-timeout` (AI-20260707-000712-008), both perf-drastic items
needing the same port-65433/65434 TPC-H runner server setup (see
`bench/tpch/env_goopg.sh` for canonical PGDATA/port/superuser env, and
`scripts/goopg-test-run.sh` for the memory-capped launch wrapper — always
pass `--hba "${PGDATA}/pg_hba.conf"`, confirmed necessary this loop).
Q9/Q20 are likely genuine planner/executor performance investigations (not
tooling bugs like Q15b was) — budget accordingly, they may take multiple
loops like the pgbench/nightly buffer-pool investigation did.

In-flight: none. The manually-started verification server (scope
`goopg-q15-verify`, port 65433, `bench/tpch/runtime_goopg/data`) was stopped
via `goopg stop -D` this loop; `ps aux` confirms no leftover
`goopg-bench-bin` process. `git status` shows only `cmd/tpch-runner/main.go`
and `.ralph/fix_plan.md`/`.ralph/working_set.md` modified — no other WIP.
