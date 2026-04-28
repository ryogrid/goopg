# 0003 — Authentication

- **Status:** accepted
- **Date:** 2026-04-28
- **Supersedes:** —

## Context

Milestone 3 in `.ralph/fix_plan.md` requires three things in order:

1. End-to-end `trust` auth driven by a `pg_hba.conf`-style file.
2. `password` (cleartext) and `md5` auth.
3. `scram-sha-256` auth (the preferred default).

Today's `internal/server/handleStartup` unconditionally answers
`AuthenticationOk`. That is fine for the v0 handshake plumbing but is
the *only* thing standing between an arbitrary client and a future SQL
executor. Replacing it with a policy-driven decision is the smallest
move that lets every later auth method drop in without restructuring
the server.

This loop lands the policy + parser + the `trust` and `reject` outcomes.
Password / MD5 / SCRAM each land in subsequent loops, each as a thin
addition to the same `Method`/`Decision` interface defined here.

References into upstream:

- `postgres/src/include/libpq/hba.h` — `UserAuth`, `ConnType`,
  `IPCompareMethod` enums.
- `postgres/src/backend/libpq/hba.c:1328` — `parse_hba_line` is the
  authoritative parser. We don't port it line-for-line, but the field
  order, keyword vocabulary, and ordering semantics come from there.
- `postgres/src/backend/libpq/hba.c:2531` — `check_hba` walks the
  parsed list in order and returns the first matching entry. Order
  matters: matching is first-match, not best-match.
- `postgres/src/backend/libpq/auth.c:379` — `ClientAuthentication`
  dispatches to the per-method handler once `check_hba` has chosen.
- `postgres/src/backend/libpq/pg_hba.conf.sample` — the canonical
  example, including the default-`scram-sha-256` recommendation.

## Decision

### Scope of this loop

Land the file format, the in-memory representation, the matcher, and
the call site in `handleStartup`. Implement only `trust` and `reject`
methods. Every other upstream method is acknowledged in the parser
(`Method` enum is comprehensive) but produces an `ErrorResponse`
explaining "auth method X not yet implemented" today.

This split keeps the diff small enough to review, leaves no
half-finished implementations of password / md5 / scram lying around,
and gives the next loop a clear seam (one `case` arm per new method).

### File format

`internal/auth` parses a subset of `pg_hba.conf`. Each non-blank, non-
comment line is one of:

```
local         <database> <user> <method> [option=value ...]
host          <database> <user> <address> <method> [options]
hostssl       <database> <user> <address> <method> [options]
hostnossl     <database> <user> <address> <method> [options]
```

Plus the include directives: `include`, `include_if_exists`, `include_dir`.

The v0 parser implements:

- Connection type: `local`, `host`, `hostssl`, `hostnossl`. The two
  GSS-encryption types are recognised as keywords but produce a
  diagnostic and the line is dropped (TLS itself isn't in v0).
- `<database>`: `all`, the literal `replication`, `sameuser`,
  `samerole`, or one or more comma-separated names. Regex match
  (`/...`) and `@filename` includes are deferred — they parse but the
  matcher fails closed with a clear error if used.
- `<user>`: `all`, `+groupname` (deferred), regex (deferred), or one
  or more comma-separated names.
- `<address>`: `all`, `samehost`, `samenet`, an IPv4/IPv6 address with
  a `/CIDR` suffix, or an IP address followed by a separate netmask
  field (the legacy two-token form).
  Hostnames and the dot-prefixed suffix form are deferred.
- `<method>`: every name in the upstream `UserAuthName` array is
  recognised by the parser and stored in the `Method` enum so future
  loops only need to wire dispatch logic. v0 *applies* only `trust`
  and `reject`.

Quoted strings (`"all"` losing its special meaning) are honoured.
Comma-separated lists in the database and user fields are split.

### Include directives

`include FILE` and `include_if_exists FILE` are honoured at parse
time, expanding the rules in place. `include_dir DIR` reads `*.conf`
files in lexicographic order. Cycle detection is by absolute path
seen-set; a cycle aborts parsing with a clear error. Paths are
resolved relative to the including file's directory, matching upstream.

### Matcher

`Match(req)` walks the rule list in order. `req` carries:

- `ConnType`: derived from the listener that accepted the connection
  (TCP today: `host`; if TLS lands, `hostssl`/`hostnossl` accordingly).
- `RemoteAddr`: the client's IP, or `nil` for a Unix socket
  connection (the `local` connection type).
- `Database` and `User`: from the `StartupMessage`.

The matcher returns the first rule whose ConnType, database, user,
and address all match, plus the rule's chosen `Method` and `Options`.
If no rule matches, the matcher returns an implicit-reject decision
identical to the explicit `reject` method. This mirrors
`check_hba`'s behavior in `hba.c:2585` (the implicit reject at the
end).

### Server integration

`internal/server/handleStartup` no longer hard-codes `AuthenticationOk`.
Instead, after parsing the StartupMessage:

1. Build an `auth.Request` from the connection metadata.
2. Call `policy.Match(req)`.
3. Run `auth.Authenticate(decision, conn, params)`. v0's
   implementation:
   - `trust` → write `AuthenticationOk`.
   - `reject` (or implicit reject) → write a FATAL `ErrorResponse`
     with SQLSTATE `28000` (`invalid_authorization_specification`)
     and close the connection.
   - Anything else → write a FATAL `ErrorResponse` with SQLSTATE
     `0A000` (`feature_not_supported`) explaining which method is
     unimplemented and close the connection.

Future loops add password / md5 / scram by implementing one new
`AuthenticationRequest` exchange each, and adding a case arm. The
function signature does not change.

### Default policy

When `--hba` is not supplied, v0 uses an in-memory policy equivalent
to:

```
host all all 127.0.0.1/32 trust
host all all ::1/128      trust
```

Loopback `trust`, everything else implicit reject. This matches the
posture of an `initdb` invocation that did not set a password and is
the safest default that still lets the existing test harness connect.

The CLI grows `goopg start --hba <path>` so an operator can point at
a real `pg_hba.conf`. Reload-from-SIGHUP handling is deferred to the
control-socket work in milestone 7; today the file is read once at
startup.

### Configuration knobs

`server.Config` grows a `Policy auth.Policy` field (an interface). The
default policy described above is constructed by
`auth.DefaultPolicy()`. Tests can construct a fixed policy directly
without going through the file parser.

### What this doc does NOT cover

- The actual `password` / `md5` / `scram-sha-256` exchanges. Each
  gets its own follow-up doc (or, more likely, an addendum to this
  one) when implemented.
- TLS / `hostssl` enforcement. v0 always answers `'N'` to SSLRequest;
  rules with `hostssl` are matched but never produce a passing
  outcome until TLS lands.
- `pg_ident.conf` and ident map name translation. Not needed before
  `peer`/`ident` methods land.
- User membership lookup (`+groupname`) — punted until a real
  user/role catalog exists.
- Per-method options (`clientcert=`, `map=`, `ldapserver=`, etc.).
  v0 parses them as an opaque `map[string]string` so future loops
  can interpret them without changing the parser.

## Alternatives Considered

- **Skip HBA in v0 and keep unconditional AuthenticationOk.**
  Rejected: every later milestone wants a typed `Decision` flowing
  out of the startup path. Adding the seam now is cheaper than
  retrofitting it.
- **Use a third-party HBA parser.** Rejected: there isn't a credible
  Go library for `pg_hba.conf` syntax, and the syntax is small enough
  that ownership is the right choice. The full upstream parser is in
  `hba.c` and includes regex / @file include / ident map handling we
  don't need yet.
- **Embed the policy as a Go data structure only (no file parser).**
  Rejected: the spec explicitly calls for a `pg_hba.conf`-syntax
  file. Operators reasonably expect to drop the file in the data
  directory and have it Just Work.
- **Implement scram first because it's the preferred default.**
  Rejected: scram is the heaviest of the three (RFC 7677 channel
  binding, SASLContinue/SASLFinal exchange, RFC 4013 stringprep). It
  needs a working policy + a working dispatcher to plug into. Doing
  `trust` first means the moving parts get exercised one at a time.

## Consequences

- The unconditional `AuthenticationOk` path is gone. Every connection
  now passes through the policy. Existing tests that just dial and
  expect a successful handshake stay working because the default
  policy trusts loopback.
- The seam is small and forward-compatible: each new auth method is
  an additional `case` in `auth.Authenticate` plus its own
  message-exchange helper. The policy engine doesn't change.
- Once TLS lands, the `hostssl` / `hostnossl` distinction will become
  load-bearing. Until then, rules using either keyword match TCP
  connections regardless of TLS state, with a `hostssl` rule on a
  cleartext connection treated as a non-match (consistent with
  upstream when TLS is unavailable).
