# Working set — M-NIGHTLY race/internal/initdb (LANDED)

**Task:** M-NIGHTLY item `race/internal/initdb`
(AI-20260815-011722-001 + AI-20260816-005117-001) — two consecutive nightly
race-stage FAILs. Selected per the Current Priority banner (M-NIGHTLY
regression fixes precede M0134).

**Diagnosis (confirmed, not a code bug):** NOT a `DATA RACE` — a per-test-binary
`-timeout` exhaustion. `go test -timeout` applies per binary, and
`internal/initdb` is internally sequential (only `relcache_init_test.go` calls
`t.Parallel()`), so the stage's `GOFLAGS=-p=4` buys it nothing. 122 call sites of
the full on-disk `initdb.Init(...)` bootstrap (`internal/initdb/initdb.go:1331`)
across 38 files at ~27-29s each under `-race` ⇒ ≈50-70 min vs the nightly's 45m
(`FAIL ... internal/initdb 2700.053s`).

**Landed:** `make race-gate` now shards any package in `RACE_SHARD_PKGS`
(default `internal/initdb`) into `RACE_SHARDS` (4) concurrent
`go test -race -run <regex>` invocations over a disjoint round-robin partition of
`go test -list`; every other package keeps the single bulk run. Policy is
**re-partition, never de-scope** — no test skipped, no `RACE_EXCLUDE` entry, no
`testing.Short()` gate, no raised global timeout. Two gate-failing self-checks:
per-shard counts must sum to the `-list` count, and an empty/failed `-list` for a
listed package is a hard error (closes the hole where a compile break would make
every shard "0 tests, skipping", the sum-check compare 0==0, and the gate exit 0
having run nothing). `RACE_SHARD_ONLY=1` times the shard set alone.

**Files:** `Makefile` (race-gate + RACE_SHARD_* vars, comment block cites the
AI-ids), `ci/batch/lib/summarize.py` (race `repro:` template 15m → the real 45m —
the stale literal misled two triage rounds), `ci/design/02-test-selection.md`
(new §"Per-package sharding" + runtime table), `.ralph/fix_plan.md` (item ticked).

**Key symbols:** `race-gate` target; `RACE_SHARD_PKGS` / `RACE_SHARDS` /
`RACE_SHARD_ONLY`; the `awk 'NR % n == (s % n)'` partition; `LISTRC` guard.

**Gates run:** measured sharded run under the nightly cgroup envelope —
152+151+151+151 = 605 tests, **≈19m56s**, slowest shard 1154s, all PASS (was a
45m timeout); `go build ./...` PASS; `RALPH_PRECOMMIT_SCOPE=units
scripts/ralph-precommit-test.sh` PASS; failure-propagation and empty-list-guard
demos both exit non-zero; pre-commit pgbench smoke PASS. No tpch-spotcheck — the
diff touches no engine code (Makefile + CI summarizer + docs only).

**Next step (NEXT LOOP — re-read the fix_plan banner first):** no M-NIGHTLY item
is open, so the banner points at **M0134** (regress-sql digestion). For
`alter_table.sql` no named correctness class remains; cheapest work in order:
(a) ledgered **C9 residuals** — already-a-partition 42809 re-ATTACH guard
(alter_table.sql:2697), ADD CONSTRAINT duplicate-name merge accounting,
ONLY-guards for SET NOT NULL / ADD CONSTRAINT; (b) the formatter tail
C7/C12/C13/C14 — measure which owns most of the 4048 diff lines before picking.
C11b (`to_json` family) and C11c (ruleutils deparser) stay DEFERRED.

**Deferral ledger:** no new row — CI infrastructure, no PG semantics left
unimplemented.

**Delegation:** researcher `tmp/ralph-handoffs/nightly-initdb-race-budget/`
(DONE, 1 round); implementer `tmp/ralph-handoffs/nightly-initdb-race-shard/`
(DONE, 2 rounds — round 2 closed the silent-pass hole found in coordinator review).

**In-flight:** none. (Note: an unrelated nightly batch run was live in this tree
during the loop, forked before these edits — left untouched; it will exercise the
pre-change recipe, so the FIRST nightly to validate the sharding is the next one.)
