# Working set — M0134-0024 PARKED; INHERITS search_path fix shipped

**Task:** M0134-0024 (`generated_virtual.sql`) — **PARKED** (case still FAILS).
Design: `docs/design/m0134-0024-inherits-searchpath-lookup.md` (indexed). CSV row
unchanged (`failed` -> `failed`), so **no `make regen-testport` this loop**.

**The method note that mattered (carry it) — now NINE loops running.** Round 1
recommended the INSERT/UPDATE generated-column bucket and left the biggest
cascade "unexplained". Interrogation did two things: it resolved an apparent
self-contradiction in that recommendation (goopg EXCLUDES generated columns yet
emits arity errors — all 18 turned out to pull the SAME direction), and it
forced the unexplained bucket to be isolated **by live experiment**, which
yielded a smaller, zero-landmine, higher-yield fix. Keep interrogating; it has
now changed the work materially nine loops in a row.

**The generalisable lesson — the bug in a case is often not ABOUT the case.**
`generated_virtual.sql` is a generated-columns file, but its dominant cascade was
a plain `search_path` bug in INHERITS parent lookup, reproducible with a table
that has no generated columns at all. It only surfaced here because the file runs
under a non-public schema. Sizing by *file topic* would have missed it entirely;
sizing by *root cause, isolated experimentally* found it. Corollary reaffirmed:
a plausible-looking site that has never executed is not evidence — the
reproduction was required before the fix was accepted.

**What shipped:** `internal/executor/operators_ddl.go:1931` (CREATE TABLE ...
INHERITS) and `:9803` (ALTER TABLE ... INHERIT) now call the pre-existing
`(o *ddlOp) lookupTableWithSearch` (M0097-0022, written for LOCK TABLE, never
wired here) instead of the raw `Catalog.LookupTable`. Qualified names provably
unaffected (helper tries the raw lookup first). PG oracle: `RangeVarGetRelid` in
`DefineRelation`, `postgres/src/backend/commands/tablecmds.c:868`.
Guard: `TestInheritsUnqualifiedParentHonoursSearchPath` — asserts inheritance
**actually works** (child columns match parent; row inserted into child visible
via the parent), not merely that the DDL stopped erroring.

**Sizing:** 4438 lines / 114 `^+ERROR` / 102 `^-ERROR` at HEAD -> **4397 / 96 /
102**. Case does NOT pass and is far from it.

**Three deferral rows appended** (2026-08-20, M0134-0024): ~25 sibling raw-
`LookupTable` DDL sites in two classes (**do the SILENT-degradation class first**
— ATTACH/DETACH PARTITION, identity-sequence heap sync, ALTER SEQUENCE lock,
GRANT bookkeeping — it loses a guarantee with no visible error); Bucket 1
(implicit INSERT/UPDATE target list excludes `GeneratedAlways`) with the proof
that its two sites must move ATOMICALLY plus the non-trailing-column positional
landmine; and `VIRTUAL` being silently treated as `STORED`.

**Next step:** select **M0134-0025 (`groupingsets.sql`)** — re-read the fix_plan
banner first (sole ordering authority; its pointer was refreshed this loop). Its
CSV status is `failed`, so apply the standing rule: re-run
`scripts/pg-regress-runner.sh --verbose groupingsets` at HEAD FIRST and let the
result decide whether the row is stale or gets sized into buckets with exact NET
grep counts. Then interrogate the park verdict once, as always.

**Gates run:** new guard test PASS (FAIL-pre proven via `git stash`:
`42P01: relation "plain1" does not exist`); `go build ./...` + `go vet
./internal/executor/...` clean; `go test ./internal/executor/...` PASS (6.7s);
`RALPH_PRECOMMIT_SCOPE=units scripts/ralph-precommit-test.sh` PASS
(`internal/initdb` 427s cache-cold, not a regression signal);
`scripts/tpch-spotcheck.sh` PASS with **Q12=2 / Q13=35** exactly.

**Delegation:** `tmp/ralph-handoffs/M0134-0024a` (researcher, sizing + 1
interrogation round, 2 rounds, DONE), `M0134-0024b` (implementer, 1 round, DONE
— report persisted by me, the worker returned it in-message), `M0134-0024c`
(tester, gates + re-measure, 1 round, DONE).
**In-flight:** none.
