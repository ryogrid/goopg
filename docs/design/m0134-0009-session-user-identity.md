# m0134-0009 — Session identity: `current_user` / `session_user` / `current_role`

Status: accepted — 2026-08-19
Milestone: M0134-0009 (`select_views.sql` regress-sql digestion)

## Problem

`internal/executor/expr.go:12361-12364` resolved `current_user`, `session_user`
and `user` to the *hardcoded string literal* `"postgres"`:

```go
case "current_user", "session_user":
    return NewStringDatum("postgres"), nil
case "user":
    return NewStringDatum("postgres"), nil
```

Consequences observed while sizing `select_views.sql` (handoff
`tmp/ralph-handoffs/M0134-0009a/report.md`):

- `SET SESSION AUTHORIZATION regress_alice; SELECT current_user;` still answers
  `postgres`.
- Every "leaky view" query in `select_views.sql` filters `WHERE name =
  current_user`, so all of them return 0 rows where PG returns 1-3. This single
  bug accounts for essentially the whole leaky-view section of the 1558-line
  diff, plus knock-on `EXPLAIN` plan-shape divergence.
- The same hardcoded fallback exists in `currentDDLOwnerName` /
  `currentDDLOwnerOID` (`internal/executor/operators_ddl.go:936-955`), so every
  `... OWNER TO CURRENT_USER` DDL site inherits it.

The login user is **already captured** at connect time into the
`session_authorization` GUC (`internal/postmaster/server.go:1184-1187`) — it was
simply never threaded into the executor `Context`.

## PostgreSQL 18.3 semantics (the oracle)

- `session_user` is the authenticated login role. It changes **only** via
  `SET SESSION AUTHORIZATION`, and is restored by `RESET SESSION AUTHORIZATION`.
  `postgres/src/backend/utils/init/miscinit.c:SetSessionAuthorization`,
  `postgres/src/backend/commands/variable.c:assign_session_authorization`.
- `current_user` / `current_role` / `user` are the **effective** role: the
  `SET ROLE` target when one is active, otherwise `session_user`.
  `postgres/src/backend/utils/init/miscinit.c:GetCurrentRoleId` — the
  `SetRoleIsActive` flag is what distinguishes "no SET ROLE" from "SET ROLE to
  the session user".
- `SET SESSION AUTHORIZATION` additionally **resets any active `SET ROLE`**
  (miscinit.c: `SetSessionAuthorization` clears the role), which is why the two
  statements are not interchangeable.
- Both are plain GUCs (`role`, `session_authorization`) with assign hooks, so
  transactional rollback rides the ordinary GUC stack — PG has no bespoke
  mechanism here.
- `current_role` is a `RESERVED_KEYWORD` and parses without parentheses, exactly
  like `current_user`.

## Design

### 1. Thread the login user into the executor Context

Add `Context.SessionUser` (the authenticated startup-packet role). It is
populated at the same site that already seeds the `session_authorization` GUC
(`internal/postmaster/server.go:1184-1187`) so the two cannot drift.

`SessionUser` is the **fallback identity**: when nothing else is set,
`session_user` and `current_user` both resolve to it. An empty value falls back
to `"postgres"` so a Context built outside the postmaster (unit tests, internal
callers) keeps today's behaviour.

### 2. Separate `SET ROLE` from `SET SESSION AUTHORIZATION`

Today `internal/postmaster/dispatch.go:355-364` wires
`ectx.SetRole = ectx.SetSessionAuthorization` — the two statements are literally
the same closure, which is why PG's distinction cannot be expressed. They are
split:

- `SET SESSION AUTHORIZATION <r>` → sets `SessionUser = r` **and** clears the
  active role (PG parity, miscinit.c).
- `SET ROLE <r>` → sets the effective role only; `session_user` is untouched.
- `RESET`/`SET ... TO DEFAULT` on either restores the connect-time login user /
  clears the role respectively.

goopg already has the rollback-aware effective-role field
(`Context.NonSuperuserRole`, `internal/executor/context.go:26-885`); this design
keeps it as the effective-role carrier rather than introducing a second store.

### 3. Resolve the value functions

In `expr.go`:

- `session_user` → `SessionUser`
- `current_user`, `current_role`, `user` → effective role if a `SET ROLE` is
  active, else `SessionUser`

### 4. Siblings that must move together (Hard-won Rule #2)

- `internal/postmaster/dispatch.go:355-364` **and** the string-matching fast path
  at `internal/postmaster/query.go:232-342` — the fast path bypasses dispatch, so
  a fix in only one leaves the other on the old behaviour.
- `currentDDLOwnerName` / `currentDDLOwnerOID`
  (`internal/executor/operators_ddl.go:936-955`) share the hardcoded fallback and
  must consult the same resolver.
- Parser: `IsNoParenFuncName` (`internal/parser/select.go:4256-4265`) is missing
  `"current_role"` although the lexer already classes it `RESERVED_KEYWORD`
  (`internal/parser/sqlkeywords/keywords.go:58`).
- **Two further siblings found during implementation**, not in the original
  analysis: `internal/postmaster/dispatch_extended.go` and
  `internal/postmaster/extended.go` also aliased
  `SetRole = SetSessionAuthorization`. Without splitting them the extended
  protocol — which is what most drivers use — kept the old conflated behaviour.
- **Three more siblings found by adversarial review** (see "Review" below):
  `internal/postmaster/conn_tx.go` (`SnapshotLocalRoleIfNeeded` / `End`, the
  `SET LOCAL` twin), `internal/executor/parallel_worker_ctx.go` (worker Context
  copy), and `internal/postmaster/copy.go` (the SELECT<->COPY twin, which
  hand-builds an `executor.Context`). Every one of them had to learn the new
  fields; each miss was a silent wrong-answer path, not an error.

### 5. The `""` sentinel — as-implemented correction

The original design assumed `Context.NonSuperuserRole == ""` unambiguously meant
"no role set". It does not: `internal/executor/operators_utility_settings.go`
collapsed the *literal role name* `postgres` into the same `""` call as
`NONE`/`DEFAULT`/`RESET`, while `query.go`'s fast path passed `postgres` through
as a real value — so the two siblings disagreed on
`SET SESSION AUTHORIZATION postgres` for a non-`postgres` login, and
`applySetRole` reproduced the same collapse independently.

The implemented shape keeps the `SetRole` / `SetSessionAuthorization` signatures
and instead makes the *state* unambiguous:

- the `"POSTGRES"` collapse is removed from both `operators_utility_settings.go`
  and `query.go`'s `applySetRole`; the literal value reaches the closures;
- an explicit-`postgres` case sets `NonSuperuserRole = ""` (preserving the
  existing convention that `""` means superuser for the ACL gates, which this
  slice deliberately does not touch) **and** `SetRoleIsActive = true`;
- `EffectiveUserName()` reads that combination — `NonSuperuserRole == "" &&
  SetRoleIsActive` — as `"postgres"` rather than falling back to the session
  user.

`EffectiveUserName()` is therefore **not** gated on `SetRoleIsActive` (the
original design said to gate it; doing so broke ten pre-existing tests that
hand-build `Context{NonSuperuserRole: "alice"}`). It is "NonSuperuserRole if
non-empty, else the explicit-postgres case, else SessionUserName()", and it
depends on the invariant — now documented on the method itself —

> `!SetRoleIsActive` ⇒ `NonSuperuserRole ∈ {"", SessionUser}`

`SetRoleIsActive` additionally makes `RESET ROLE` a no-op when no `SET ROLE` was
ever active, which is PG-correct: `postgres/src/backend/utils/misc/guc.c:4092-4127`
forces `SET ROLE NONE` on `SET SESSION AUTHORIZATION` and `RESET role` on its
reset. (Note the authority is guc.c, **not**
`miscinit.c:SetSessionAuthorization` — that function deliberately does *not*
clear `SetRoleIsActive`, being documented as commutative with
`SetCurrentRoleId`.)

### 6. Transactional scope

`SET LOCAL` must revert the new state at COMMIT/ROLLBACK exactly as it already
did for `NonSuperuserRole`: `internal/postmaster/conn_tx.go`
`SnapshotLocalRoleIfNeeded` / `End` snapshot and restore `SessionUser` and
`SetRoleIsActive` alongside `LocalRolePriorValue`. Omitting this leaks
`SET LOCAL SESSION AUTHORIZATION` past COMMIT for the remaining life of the
connection — a silent wrong answer, not an error.

## Review

An adversarial review of the first implementation returned DO-NOT-SHIP and is
the reason §5 and §6 exist. Its durable lesson is the sibling audit: the brief
named two paths (`dispatch.go`, `query.go`), implementation found two more
(`dispatch_extended.go`, `extended.go`), and review found three more still
(`conn_tx.go`, `parallel_worker_ctx.go`, `copy.go`). **Adding a field to
`executor.Context` is a seven-site change in this codebase**, and every missed
site was a silent wrong answer rather than a compile error — the field simply
stayed at its zero value and fell back to `"postgres"`. Any future
`Context`-state addition should start from that list.

The review also showed that a passing test proves nothing about *which* path it
exercised: `TestDispatchPathSessionUserIdentity` sent its SETs over the extended
protocol (intercepted by `extended.go`'s string-matching fast path) and its
SELECT over the simple protocol, so the dispatch closures it was named for had
literally zero coverage. Reaching them requires the multi-statement simple-query
shape (`SET ROLE alice; SELECT current_user;`) that `query.go` routes to
`dispatchSimpleQueryViaExecutor` — the shape psql `-c` and many drivers send.
Coverage profiles, not test names, settled it.

## Explicitly out of scope (deferred — see `.ralph/deferral_ledger.md`)

- **Role-existence and privilege validation.** PG's
  `check_session_authorization` (variable.c:814-921) rejects a non-existent role
  with `role "%s" does not exist` and requires superuser to assume another role.
  goopg accepts any name.
- **GUC-stack transactional rollback fidelity** for `role` /
  `session_authorization`; `role` is not even a registered GUC
  (`internal/utils/misc/defaults.go`), so `current_setting('role')` does not see
  it.
- **EXPLAIN deparse.** `internal/executor/operators_explain.go:1184-1189`
  unconditionally parenthesizes a FuncCall, rendering `CURRENT_USER` as
  `current_user()`; the no-paren guard pattern already exists at
  `internal/catalog/catalog.go:20425-20438`.

## Why `select_views.sql` still does not close

This fix removes the dominant divergence but the case remains blocked on two
parser gaps found during sizing: the `?#` geometric containment operator is not
tokenized at all (`?` is not an operator-start character), and unary prefix `#`
(path point-count) is unsupported — both are needed by the `street` / `iexit`
views in `create_view.sql`. `create_view.sql` also silently no-ops the
`CREATE SCHEMA ... CREATE TABLE ...` sub-command form. The CSV row therefore
stays `failed`; see the M0134-0009 task for the re-arm trigger.
