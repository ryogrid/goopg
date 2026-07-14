# 0097-0151 — `DateStyle` partial-`SET` merge semantics

Status: accepted
Milestone: M-NIGHTLY follow-up (source: `.ralph/deferral_ledger.md` 2026-07-14
row "M-NIGHTLY (run 20260714-011651 follow-up)")
Date: 2026-07-14

## Problem

`DateStyle` packs two independent components into one GUC string: a display
STYLE (`ISO`/`SQL`/`Postgres`/`German`) and a field ORDER (`YMD`/`DMY`/`MDY`),
e.g. `"ISO, MDY"`. A `SET datestyle = ...` is allowed to name only one
component — `SET datestyle = 'SQL'` changes just the style and must **keep**
the session's current order, matching `check_datestyle`
(`postgres/src/backend/commands/variable.c`).

Before this fix, `DateStyle` was a plain `TypeString` GUC: `canonicalize`
returned the input unchanged with no parsing at all. Consequences:

- `SET datestyle = 'SQL'` stored the literal string `"SQL"` — losing the order
  component entirely, so `SHOW datestyle` no longer even reported a valid
  order, breaking any code (or regress test) that expects the canonical
  `"<Style>, <Order>"` shape.
- `SET datestyle = 'ISO, SQL'` (two conflicting styles) or
  `SET datestyle = 'bogus'` (an unrecognized keyword) were silently accepted —
  PostgreSQL rejects both (`"Conflicting \"DateStyle\" specifications"` /
  `"Unrecognized key word"`).
- `GERMAN`'s implicit `DMY` order default (unless the same `SET` also names an
  order) was not implemented.

This is a real client-visible protocol/GUC-correctness bug, independent of
and narrower than the separately-tracked, much larger gap of actually
rendering date/timestamp *output* in the non-ISO styles (goopg's date/time
formatters are hardcoded to one literal layout regardless of the `DateStyle`
GUC — see the same 2026-07-14 ledger row's "corrected quick suite
classification" entry; that output-rendering project is intentionally **not**
touched here).

## Fix

`internal/config/datestyle.go` (new file):

- `parseDateStyleValue(s) (style, order string)` extracts the two components
  from an already-canonical `"<Style>, <Order>"` string (falls back to
  `ISO`/`MDY` for a component the string doesn't mention, so a malformed
  `current` never panics).
- `mergeDateStyle(current, bootVal, newValue) (string, error)` ports
  `check_datestyle` token-for-token: splits `newValue` on `,`, and each token
  sets either the style or the order, starting from `current`'s components
  (so an unspecified component survives). `GERMAN` also sets `DMY` unless the
  same call already saw an order token. `DEFAULT` recursively resolves
  against `bootVal`. A second token for an already-set component that
  disagrees is a conflict error; an unrecognized token is a keyword error.
  Returns the canonical `"<Style>, <Order>"` form, matching
  `assign_datestyle`'s `guc_malloc`'d result.

`internal/config/guc.go`:

- `Variable.canonicalize(value)` is now a thin wrapper over new
  `Variable.canonicalizeFrom(current, value)`, which special-cases
  `strings.EqualFold(v.Name, "DateStyle")` to call `mergeDateStyle` before
  falling through to the original by-`Type` switch for every other GUC.
  `canonicalize` passes `v.Value` as `current` — correct for every existing
  call site (`Variable.Set`, `Registry.ApplyReloadEntries`, `setFromFile`,
  `NewVariable`'s boot-value pass), where `v.Value` already *is* the current
  effective value.

`internal/config/session.go`:

- `SessionRegistry.Set` and `SessionRegistry.SetInternal` now fetch the
  session's **effective** current value via `s.Get(name)` first and call
  `v.canonicalizeFrom(current, value)` instead of `v.canonicalize(value)`.
  This distinction matters only for session/transaction-scoped GUCs: `v` is
  the shared global `*Variable`, so `v.Value` reflects the *global* default,
  not a prior `SET`/`SET LOCAL` override in this session. Merging a partial
  `SET datestyle = 'DMY'` against the stale global value instead of the
  session's actual current setting would silently discard an earlier
  `SET datestyle = 'SQL'` in the same session.

Every other GUC's `canonicalizeFrom` behavior is byte-for-byte identical to
the old `canonicalize` (the `current` parameter is ignored outside the
`DateStyle` branch), so this is a zero-behavior-change refactor for the rest
of the GUC table.

## Verification

- New `internal/config/datestyle_test.go`: partial-SET order preservation
  (`SQL` after a prior `DMY` keeps `DMY`), `GERMAN` implying `DMY` unless
  overridden, conflicting-spec rejection, unrecognized-keyword rejection,
  `DEFAULT` token merging against boot value, boot-value round-trip.
- Live end-to-end check against a real `cmd/goopg` binary via `psql`:
  `SET datestyle = 'SQL'` → `SHOW datestyle` = `SQL, MDY`; a subsequent
  `SET datestyle = 'DMY'` → `SQL, DMY` (order-only SET correctly preserved
  the just-set style); `SET datestyle = 'ISO, SQL'` →
  `ERROR: conflicting "datestyle" specifications`; `SET datestyle = 'nonsense'`
  → `ERROR: unrecognized key word: "nonsense"`.
- `go test ./internal/config/... ./internal/server/... ./internal/executor/...`
  all PASS (no regression in the shared `canonicalize` path used by every
  other GUC).
- `scripts/tpch-spotcheck.sh` PASS (Q12=2/Q13=33).
- `RALPH_PRECOMMIT_SCOPE=smoke scripts/ralph-precommit-test.sh` PASS (0 failed
  transactions, all 3 pgbench workloads).

## Scope / what's still open

This fix does not flip the `guc`/`date`/`timestamp`/`timestamptz`/`horology`
regress suites to `pass` — their remaining divergence is the separately
tracked date/timestamp *output rendering* gap (goopg's formatters ignore the
`DateStyle` GUC entirely; see the 2026-07-14 ledger row). A deferral-ledger
row records that as the next step. Also out of scope (pre-existing, unrelated
to `DateStyle`): `SET LOCAL <var> = ...` issued outside an explicit
transaction currently persists until the next `SET`/`RESET` in this session,
whereas real PostgreSQL scopes it to only the current implicit
single-statement transaction (visible in the same `guc` regress diff via
`vacuum_cost_delay`, not touched by this change).
