# Working set — M0134-0018 PARKED; shipped a silent catalog-loss fix out of it

**Task:** M0134-0018 (`create_index.sql`) — **PARKED** (case still FAILS; CSV row
stays `failed`/`pass_required=no`, **no `make regen-testport`**). The loop's
shipped work is a general correctness fix found while sizing it.
Design: `docs/design/m0134-0018-temp-shadow-drop-rollback.md` (indexed).

**The method note that mattered this loop (carry it).** The researcher's first
verdict was again "PARK, no bucket is CLOSE-sized" — and it was right about the
case. But its own report *buried* the real find in an "anomaly flagged, not
resolved" aside: `public.point_tbl` vanished from `pg_class` mid-case with no
error. One follow-up round demanding a bisection (which statement, catalog-loss
vs data-loss, session-scoped vs cluster-wide, restart behavior, root-cause
file:line) turned that aside into the loop's entire deliverable. **Interrogating
a park verdict has now paid off two loops running — and this time the prize was
not in the recommendation, it was in the footnote. Read subagent reports for what
they under-rate, not just for what they conclude.**

**The bug.** `CREATE TEMP TABLE zz AS SELECT * FROM public.zz` (goopg's
`create_index.sql:84`) errored *and* permanently deleted `public.zz` from the
live catalog for every session until restart. `execCreateTable`
(`operators_ddl.go:1713`) implements TEMP shadowing (M0097-0003) by destructive
pre-emption: stash the permanent table in `TempTableShadows`, `DropTable` it
(`:1750-1766`), restore only from `DROP TABLE` on the temp relation
(`:6936-6945`). That assumes the CREATE succeeds; the self-shadowing CTAS
guarantees it does not — the drop removes the SELECT's own source, `optimizer.Plan`
fails at `:4714-4716` before any `CreateTable`, no temp relation exists, so the
restore is unreachable. Loss is catalog-entry-only (heap file intact),
cluster-wide (`catalog.InMemory` is shared), and heals on restart — hence silent.
**Fix:** named-error-return `defer` in `execCreateTable` restoring via a new
shared `restoreTempShadow` helper (the DROP path was rewired to it, so there is
ONE notion of "undo a shadow"), covering every post-drop error exit; success path
bit-identical.

**Sizing (clean, `--no-setup`):** 3475 lines / 43 hunks / 112 `^+ERROR`. The
runner's setup phase runs `create_index.sql` once already (prereq for
`aggregates.sql`) then again as the test — that double-run adds ~28 spurious
`already exists` errors. Buckets 1 (geometric lexer, 58) + 3 (CONCURRENTLY, 25)
= 74%, both REFACTOR-tier and ledgered; fixing both still leaves 29/112.

**Three deferral rows appended** (2026-08-20, M0134-0018): namespace-keyed
catalog lookup (PG has no shadow-drop at all — `pg_temp` vs `public` coexist via
`search_path`, `namespace.c:RangeVarGetRelid`; that end state retires
`TempTableShadows` entirely and would make the statement SUCCEED); the two
parked `create_index` buckets; and `SET SESSION ROLE` (no case in the
string-prefix SET dispatcher — sibling pair `internal/postmaster/query.go` +
`extended.go`, ~15 LOC, whole `SET SESSION <any-guc>` form affected).

**Next step:** select **M0134-0019 (`indexing.sql`)** — re-read the fix_plan
banner first (sole ordering authority; its "next to select" pointer was stale by
three tasks this loop and has been refreshed). Then the standing rule: run
`scripts/pg-regress-runner.sh --verbose <case>` at HEAD BEFORE designing, size
into buckets, and interrogate any park verdict once.

**Gates run:** `go build ./...` + `go vet ./internal/executor/` clean;
`RALPH_PRECOMMIT_SCOPE=units scripts/ralph-precommit-test.sh` PASS
(`internal/initdb` cache-cold at 439s, not a regression signal);
`scripts/tpch-spotcheck.sh` PASS with **Q12=2 / Q13=35** exactly;
`go test -run TestDDL ./internal/executor/` PASS (47 subtests) incl. FAIL-pre /
PASS-post guard `TestDDLFailedTempShadowCreateRestoresPermanentTable`;
end-to-end real-server repro on port 5533 confirmed `public.zz` survives and is
visible from a SECOND connection; pre-commit pgbench smoke PASS.

**Delegation:** `tmp/ralph-handoffs/M0134-0018a` (researcher, sizing + 3
follow-ups, 2 rounds, DONE), `M0134-0018b` (implementer, 1 round, DONE),
`M0134-0018c` (tester, gates, 1 round, DONE).
**In-flight:** none.
