# 0102-0016 — initdb `-A`/`--auth`, `--auth-host`, `--auth-local`, `--pwfile`

Status: accepted
Milestone: M0102-0010 (initdb CLI option coverage)
Date: 2026-06-13

## Problem

`goopg init` writes a fixed `pg_hba.conf` that hard-codes `trust` for the
loopback/local rules and seeds the bootstrap superuser with an **empty**
`rolpassword`. Upstream `initdb` lets the operator choose the authentication
method for host and local connections (`-A`/`--auth`, `--auth-host`,
`--auth-local`) and set the superuser password from a file (`--pwfile`),
which it encodes into `pg_authid.rolpassword`. `001_initdb.pl` exercises
`--auth => 'trust'` (line 137) as part of its ICU case, and the broader
auth surface is a documented compatibility gap (M0102-0010).

This is the **seventh** initdb-option slice under M0102-0010. It closes the
`--auth`/`--pwfile` family from the milestone's "Remaining initdb options"
list.

## Upstream reference

`postgres/src/bin/initdb/initdb.c`:

- `auth_methods_host[]` / `auth_methods_local[]` (lines 96-130) — the valid
  method sets. Host allows `trust reject scram-sha-256 md5 password ident
  radius` (+ build-gated gss/sspi/pam/bsd/ldap/cert); local allows the same
  minus `ident`/`cert` plus `peer`.
- option parsing (lines 3248-3264): `-A` sets both host and local; the
  `ident`↔`peer` cross-map (3255-3258) — if `--auth=ident`, local becomes
  `peer`; if `--auth=peer`, host becomes `ident`. `--auth-host` /
  `--auth-local` set one side only.
- `check_authmethod_unspecified` (2571) — default to `trust`, set
  `authwarning`.
- `check_authmethod_valid` (2581) — `invalid authentication method "%s" for
  "%s" connections`.
- `check_need_password` (2596) — if **both** host and local are a password
  method (`md5`/`password`/`scram-sha-256`) and neither `--pwprompt` nor
  `--pwfile` was given → `must specify a password for the superuser to
  enable password authentication`.
- `setup_config` (1402-1518): seeds `password_encryption = md5` when md5 was
  chosen (unless scram was chosen for the other side, 1406-1413), and
  replaces the `@authmethodhost@` / `@authmethodlocal@` tokens in
  `pg_hba.conf.sample`; the `@authcomment@` warning is emitted when either
  side is `trust`.
- `get_su_pwd` (1656) — read the password from the first line of `--pwfile`,
  strip CRLF; `could not open file "%s" for reading`, `password file "%s" is
  empty`.
- `setup_auth` (1640) — `ALTER USER "<username>" WITH PASSWORD E'…'`, which
  the bootstrap backend encodes per `password_encryption` into
  `rolpassword`.

## Design

### Options (internal/initdb/initdb.go)

```go
AuthMethodHost  string // -A/--auth + --auth-host; "" → "trust"
AuthMethodLocal string // -A/--auth + --auth-local; "" → "trust"
PwFile          string // --pwfile: read superuser password from first line
```

`-A`/`--auth` on the CLI sets *both* `AuthMethodHost` and `AuthMethodLocal`
to the same value; `--auth-host` / `--auth-local` override one side.

### New file: internal/initdb/auth_bootstrap.go

- `validAuthMethodsHost` / `validAuthMethodsLocal` — the build-unconditional
  subset goopg accepts (trust, reject, scram-sha-256, md5, password, ident,
  radius / + peer for local). gss/sspi/pam/bsd/ldap/cert are accepted as
  valid (a real `pg_hba.conf` may name them) but, like the existing
  `internal/auth` parser, only trust/reject are actually enforced at connect
  time; initdb's job is just to write the file.
- `resolveAuthMethods(host, local string) (rHost, rLocal string, warn bool,
  err error)` — ports unspecified-default + ident/peer cross-map +
  valid-check + need-password check. The ident/peer cross-map only fires
  when a single `--auth` set both sides to the same value, matching
  upstream's parse-time behavior; `resolveAuthMethods` detects that case by
  `host == local`.
- `buildPgHBAConf(host, local string) []byte` — generates the same file
  shape goopg already wrote, with the method substituted into the
  `local`/loopback-`host` rules; the external `0.0.0.0/0` / `::/0` rules stay
  `reject`. `defaultPgHBAConf()` becomes `buildPgHBAConf("trust", "trust")`
  so existing callers/tests are unchanged.
- `readSuperuserPasswordFile(path) (string, error)` — first line, strip
  trailing CR/LF; ports `get_su_pwd`'s open/empty errors.
- `encodeSuperuserPassword(password, host, local, username string) (verifier,
  passwordEncryption string, err error)` — chooses md5 vs scram-sha-256 per
  the same rule initdb uses for `password_encryption`, returns the
  `rolpassword` verifier string (`auth.NewSCRAMSecret(...).String()` or
  `auth.MD5Shadow(password, username)`) plus the GUC value to seed (only
  non-empty when md5, since scram is the template default).

`internal/auth` is a stdlib-only leaf package (no import cycle). A new
exported `auth.MD5Shadow(password, user string)` wraps the existing
unexported `md5Shadow` so the md5 `rolpassword` form has a single source of
truth (sibling-path rule).

### Init wiring (internal/initdb/initdb.go)

1. Resolve + validate auth methods **up front**, before any filesystem work
   (alongside the existing superuser/waldir/sync-method validation), so a
   bad method or missing password aborts before the tree is created.
2. Read `--pwfile` (if set) and encode the verifier before layout.
3. After the `SampleFiles()` loop, overwrite `pg_hba.conf` with
   `buildPgHBAConf(host, local)`.
4. `seedPostgresqlConf` gains a `passwordEncryption` arg, applied between the
   text-search and group-access seeds (matching `setup_config` order), so a
   `-c password_encryption=…` override still wins.
5. `bootstrapPostgresRole` delegates to a new
   `bootstrapPostgresRoleWithPassword(dataDir, superuser, rolpassword)`; the
   OID-10 bootstrap row carries the verifier (still a non-NULL `text`, so
   `HEAP_HASNULL` stays clear and `t_hoff` is unchanged). The OS-user row and
   predefined roles are untouched.
6. Emit the trust-default auth warning to the server log when no method was
   specified (`authwarning`).

### Out of scope

- `-W`/`--pwprompt` — needs an interactive TTY; `goopg init` is
  non-interactive, so only `--pwfile` is supported. Documented as such.
- goopg's own server does not yet authenticate against `pg_authid.rolpassword`
  (it uses `internal/auth`'s user store); writing the verifier is for on-disk
  PG-compatibility (a real PG attaching to / inspecting the cluster sees the
  correct `rolpassword`). End-to-end goopg password auth is separate work.

## Testing

- `internal/initdb/auth_bootstrap_test.go`:
  - `resolveAuthMethods` table: defaults, ident→peer, peer→ident, invalid
    host method, invalid local method, both-password-without-pwfile error.
  - `buildPgHBAConf` substitution (trust default preserves the existing
    needles; scram-sha-256 substitutes host+local).
  - `readSuperuserPasswordFile` first-line/CRLF/empty/missing.
  - `Init` end-to-end with `--auth=scram-sha-256 --pwfile`: `pg_hba.conf`
    carries scram, `pg_authid` OID-10 row's `rolpassword` parses as a SCRAM
    secret and verifies the cleartext via
    `auth.SCRAMSecret.VerifySCRAMSecretFromPassword`.
  - `Init` with `--auth=md5 --pwfile`: `password_encryption = md5` seeded;
    `rolpassword` is the `md5…` form.
  - `Init` with `--auth=scram-sha-256` and no pwfile → error.
- `cmd/goopg/main_test.go`: `TestInitCommandAuthAndPwfile` (flags thread
  through; method reflected in `pg_hba.conf`).

No executor/planner/catalog-format change → the TPC-H spotcheck gate does
not apply. `internal/auth` + `internal/initdb` package tests + `go vet` +
`gofmt` are the gates.
