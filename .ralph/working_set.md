Task: M0137-0003 — write the canonical baseline-capture procedure.
**COMPLETE and committed** (pending push in this loop).

Files: `docs/design/0100-0149/m0137-0003-baseline-capture-procedure.md`
(new), `docs/design/README.md` (+index row), `docs/design/not_ralph/plan_parity_fix_take2/METHODOLOGY.md`
(§4.1 TPC-H caveat + pointer to the new doc), `AGENT.md` (two dangling
"plan-parity-take2 work appendix" pointers repointed at the new doc),
`scripts/capture-tpch.sh` (fail-fast `TPCH_QUERY_DIR`/`TPCH_Q15A_FILE`
existence guard + updated header comment), `.ralph/deferral_ledger.md`
(M0137-0001's `TPCH_QUERY_DIR` row flipped `-` -> `resolved`),
`.ralph/fix_plan.md` (M0137-0003 checked off).

Key symbols: none (docs + a shell guard; no Go code touched).

Findings:
- The AGENT.md "plan-parity-take2 work appendix" this task was meant to
  replace was already DELETED (commit `baf40efcb`, same commit that filed
  M0137-M0143) — its pointer text ("read its §0 and §1") pointed at nothing.
  Recovered its still-needed bootstrap/port/auth content from
  `git show baf40efcb -- AGENT.md` and folded it into the new design doc.
- `docs/design/not_ralph/plan_parity_fix_take2/TODO.md:4861-4875` (path named
  directly in the M0137 milestone doc's own task line, so citable per the
  harness's round-directory access rule) settles BOTH halves of this task
  with a direct 2026-09-14 probe: TPC-H's `r2-instrument/capture-tpch.sh`
  plans on empty stats (re-hit trap, same as R65 §0) because goopg's ANALYZE
  stats are per-connection and that script never ANALYZEs; TPC-DS is
  confirmed NOT affected (query7 cold-session vs ANALYZE-warmed-session
  plans shape-identical, costs within 0.03%) because its load-time ANALYZE
  persists durably. This is why `capture-tpcds.sh` stays canonical for
  TPC-DS while `estimate-audit -plan-only` is canonical for TPC-H — asymmetry
  is deliberate and cited, not assumed.
  - Went deep into the underlying mechanism first (SetTableStats storing a
    SHARED `*Table.Stats` pointer, not obviously per-connection; a live
    throwaway-server experiment on db `postgres` showed ANALYZE stats DO
    persist across fresh connections there) before finding the TODO.md
    citation above already settles it directly with a same-corpus probe.
    Do not re-open this — trust the citation, not the code archaeology.
  - Also resolved the M0137-0001 ledger row on `capture-tpch.sh`'s untracked
    `TPCH_QUERY_DIR`: decided it's off the baseline critical path now
    (estimate-audit reads `tpch.Queries()` directly), so relocating the
    corpus into git tracking is optional polish, not required. Added a
    fail-fast existence guard so a missing corpus errors once instead of 22
    silent "MISSING QUERY FILE" sections.
- Live-validated the exact `estimate-audit -plan-only` command end-to-end:
  built the binary, stood up a throwaway private goopg server (never a
  shared `:6543x` cluster), loaded `tpch.DDL()` + `tpch.SampleInserts()`,
  ran `-plan-only -queries 1,6`, confirmed `=== Qn` section format matches
  `pg-plan-parity-diff.py`'s `SECTION_RE`. Server stopped, all scratch files
  removed, nothing committed from the run (same precedent as M0137-0002's
  manual checks).

Next step: read `.ralph/fix_plan.md`'s M0137 section and select the next
topmost unchecked M0137 task per the banner — **M0137-0004** (reconcile the
TPC-DS `match=2` vs `match=1` discrepancy) is the next natural pick since
this task's own doc explicitly declines to settle that question and named it
as still open. Before starting, re-read `AGENT.md` §"Plan-parity harness"
(fresh loop = fresh session per the loop-start discipline) and this task's
new doc's §3 caveat about the reference question.

Gates run: `RALPH_PRECOMMIT_SCOPE=units scripts/ralph-precommit-test.sh` —
ran in full; same single pre-existing FAIL (`internal/parser`
`TestLockingClauseParity`, already filed under M-NIGHTLY, zero Go files
touched this loop) and nothing else. `python3 scripts/capture-idempotent-test.py
-v` — 2/2 pass (unchanged count; the new guard is exercised on its success
path by the existing fixture env). `bash -n` on `capture-tpch.sh`,
`capture-tpcds.sh`, `capture-stamp.sh` — clean. Manual guard-rail check
(missing `TPCH_QUERY_DIR` exits 1 with the new message instead of 22
"MISSING QUERY FILE" sections) — as expected, not committed. `make
ralph-state-guard` — repaired one stale running/completed mismatch from the
prior loop's clean exit (same pattern as the last two loops), then OK.

In-flight: none.
