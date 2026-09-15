Task: M0137-0017 — capture plans in BOTH serial and parallel modes (banner's
item 2, last of the re-opened 0014-0017 set). **DONE this loop**
(`a419f4922`, committed). Item 2 is now fully closed (0014-0017 all `[x]`).

Files: `analysis/m0137/m0137-0017-{serial,parallel}.{txt,plans.txt,pg.plans.txt}`
(new — committed raw captures, not /tmp), `docs/design/0100-0149/m0137-0017-serial-and-parallel-capture.md`
(new), `docs/design/README.md` (+1 row), `.ralph/fix_plan.md` (M0137-0017
checked `[x]`, banner's item-2 text annotated RESOLVED, new **M0137-0019**
task filed), `.ralph/deferral_ledger.md` (+1 row
`m0137-0017-parallel-mode-divergence`). No production Go code touched — this
was a measurement/recon task per the harness's carve-out.

Key symbols: none (no code change). The mechanism exercised is
`cmd/estimate-audit/main.go`'s existing `-serial` flag (default true,
line 293) and `session.ensure`'s `SET max_parallel_workers_per_gather = 0`
guard (line 363-369) — no code was added, `-serial=false` already worked,
it had just never been run against a live PG parallel-mode reference before.

Findings: built the first-ever parallel-mode PG 18.3 TPC-H reference
(`estimate-audit -plan-only -serial=false -ref-port 65432 ...`) alongside a
matching serial control, same stats-epoch on both
(`253df6b1d97f9f5b`, confirmed by inspecting each artefact's header — this
is what makes it a clean A/B on `-serial` alone). Parallel-mode diff:
`parallelism` category — read `0` in every prior M0137-0143 report because
the serial capture measures it *out*, per ledger row
`take3-plan-capture-is-serial-only` — is now scoreable at **16/22**, the
largest category besides `join-order`. Serial-arm match (6/22: Q1, Q6, Q10,
Q11, Q14, Q15a-VIEWBODY) does not hold under parallelism: only Q6 and Q11
survive, so parallel-mode match reads **2/22**. Q1/Q10/Q14/Q15a-VIEWBODY are
therefore the cleanest 4 to start a future triage from (parallelism-only
regressions, not compounded with a pre-existing serial-mode divergence).
Deliberately did NOT diagnose or fix any of the 16 — that's new work, filed
as M0137-0019 with its own ledger row per the group's two-artefact
completion rule (a ledger row alone does not close a task in this milestone
group).

In-flight: none. `estimate-audit` binary built to `/tmp/estimate-audit`
(not committed, per AGENT.md's "never commit a build artefact" — already
gitignored via `/estimate-audit`, and `/tmp` is outside the repo anyway).
Both TPC-H clusters (`:65432` PG, `:65433` goopg) were left running/untouched
(shared block, per AGENT.md "verify a reference, never restart it" — only
read-only EXPLAIN queries were issued).

Next step: re-read the `## Current Priority` banner fresh in
`.ralph/fix_plan.md` before trusting this note. As last edited this loop,
item 2 is fully resolved and item 3 is next: **M0138 — PG-faithful ANALYZE
statistics (0007-0009: orphaned `numeric` `avg_width`, category-shift
bisect, correlation-banding re-open)** and **M0140-0006 — TPC-DS
parallelism (the partial-Append producer M0140-0004 deferred without an
owner)**. Check whether M0138-0007/0008/0009 already exist as fix_plan
tasks under the `## M0138` section (the banner names them by number but
this loop did not verify they're filed with that exact numbering — grep
`M0138-000[7-9]` before assuming). M0137-0019 (this loop's own follow-up)
is filed but NOT next per banner priority — it's new investigative work,
sized like M0141/M0142's remaining slices, not part of the fixed
0014-0017 re-opened set.

Gates run: `go build ./...` clean (no Go files touched). Both
`estimate-audit -plan-only` captures rc=0. Both
`scripts/pg-plan-parity-diff.py` diffs rc=0, unparsed=0. Pre-commit hook's
pgbench smoke PASS (tps ~43-150, 0 failed). `make ralph-state-guard`: one
self-repair (same recurring benign stale-clean-exit-marker pattern several
prior loops have noted — status="running"/progress reconciled to
"in_progress"), clean after repair.
