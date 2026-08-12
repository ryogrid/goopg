(idle — nothing in flight)

M0131-S22 LANDED (loop #167) — CLOG replay opcode dispatch + commit-record
`subxacts[]` parsing. Both halves of the bug fixed in one slice.

Files: new `internal/wal/pg_xact_parse.go` (`XactParsed`, `ParseXactRecord`),
`internal/wal/pg_assembled_emit.go` (`EncodeXactCommitPGWithSubxacts`,
`EncodeXactAbortPGWithSubxacts`, `EncodeXactAssignmentPG`; the old two-arg
entry points delegate), `internal/initdb/xact_recovery.go` (opcode dispatch +
tree stamping), new `internal/wal/xact_parse_pg_test.go` (6 guards),
`internal/initdb/xact_recovery_test.go` (+4 guards), design
`docs/design/0131-0015-*` §"S22 — implementation notes", `docs/design/README.md`,
`.ralph/fix_plan.md`, 2 `.ralph/deferral_ledger.md` rows.

Three things to remember:
1. **Non-COMMIT/ABORT xact opcodes are SKIPPED, not refused,** in the initdb
   pass — the physical pass already refuses two-phase loudly and runs first.
2. **A truncated body is a parse ERROR** — half a decoded subxact tree is worse
   than none (decoded half committed, rest swept ABORTED). The caller keeps the
   top-level stamp and skips the tree rather than failing the start.
3. **assignment-then-commit self-heals the dispatch bug** (the commit overwrites
   the bogus abort stamp), proven by revert. The damaging case is the assignment
   whose commit was never written — every transaction in flight at the crash.

NEW RED GATE found while gating this (filed as **M0131-S21c**, ledger row, NOT
fixed here): `TestE2E_GoopgCrashStartOnPGDataDir` now HARD-FAILS at
`XLOG_HEAP2_MULTI_INSERT` — "targets already-allocated line pointer …
goopg redo has no in-place line-pointer reuse" (`internal/wal/recovery.go:3275`).
Verified identical at HEAD with the S22 diff stashed, so not an S22 regression:
the test's self-arming skip only recognises the pre-S21a NEXTOID refusal, and
S21a moved the refusal along. Upstream uses `PageAddItemExtended(PAI_OVERWRITE)`,
which reuses an UNUSED line pointer in place.

Gates: `internal/wal` + `internal/initdb` full packages PASS, `-race` on the
touched subsets PASS, UNITS precommit PASS (warm cache), pgbench smoke via the
commit hook, `make ralph-state-guard` OK. 10 guards, fail-when-broken by 2
scripted reverts (dispatch dropped → 1 FAIL, subxact walk dropped → 3 FAIL).

Nightly triage: `ci/logs/action-items.md` still run `20260812-005501`; all 4
`## AI-` items already filed under M-NIGHTLY, nothing new to file.

Next loop (banner = M-NIGHTLY filing, then M0131): **M0131-S21c** is the natural
pick — it is a red gate that S28's E2E (the milestone's own acceptance vehicle)
depends on. Otherwise S23 (the cheap tail).

In-flight: none.
