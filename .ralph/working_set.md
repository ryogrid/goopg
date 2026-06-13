Task: M0102-0010 — add the next initdb CLI option. Loop #25 landed
`-A`/`--auth`, `--auth-host`/`--auth-local`, and `--pwfile` (auth
bootstrap), satisfying `001_initdb.pl`'s `--auth=trust` usage (line 137).
Committed + pushed → idle on this slice.

Files (this loop):
- internal/initdb/auth_bootstrap.go (NEW) — resolveAuthMethods (ports
  check_authmethod_unspecified default-trust+warn, ident↔peer cross-map,
  check_authmethod_valid, check_need_password), buildPgHBAConf(host,local),
  readSuperuserPasswordFile (get_su_pwd), encodeSuperuserPassword
  (SCRAM default / md5 via auth.MD5Shadow; returns password_encryption GUC).
- internal/initdb/initdb.go — Options.AuthMethodHost/AuthMethodLocal/PwFile;
  Init validates auth up front (before filesystem), reads pwfile + encodes
  verifier, overwrites pg_hba.conf with buildPgHBAConf after SampleFiles,
  passes passwordEncryption to seedPostgresqlConf, calls new
  bootstrapPostgresRoleWithPassword (writes verifier into OID-10 row).
  defaultPgHBAConf() now delegates to buildPgHBAConf("trust","trust").
  Added "log" import for the trust-default warning.
- internal/initdb/config_seed.go — seedPostgresqlConf gained a
  passwordEncryption arg (applied before -c/--set loop).
- internal/auth/userstore.go — exported MD5Shadow wrapper (single source of
  truth for the md5 rolpassword form).
- internal/initdb/auth_bootstrap_test.go (NEW), cmd/goopg/main.go (flags),
  cmd/goopg/main_test.go (TestInitCommandAuthAndPwfile).
- docs/design/0102-0016-initdb-auth-options.md (NEW) + README index row.
- .ralph/fix_plan.md (loop #25 progress).

Key facts:
- internal/auth is a stdlib-only leaf pkg → no import cycle from initdb.
- goopg's server does NOT authenticate against pg_authid.rolpassword yet
  (uses internal/auth user store); the verifier write is on-disk PG-compat.
- -W/--pwprompt deliberately out of scope (non-interactive CLI).
- Touches ONLY internal/initdb + internal/auth + cmd/goopg → NO
  executor/planner/catalog/codec/WAL-format → TPC-H spotcheck gate N/A.
- ~19 files of FOREIGN uncommitted changes remain untouched. Commit
  selectively; never git add -A.

Next step (next loop): continue M0102-0010 with the next remaining option.
Remaining: `--encoding` (encoding catalogs), `--locale`/`--lc-*` +
`--locale-provider`/`--icu-locale` (ICU — big), `--data-checksums`
(full page-checksum write/verify path — high blast radius). Design doc first.

Gates run: gofmt clean (my files); go build ./... PASS; go vet
./internal/initdb ./cmd/goopg ./internal/auth PASS; go test ./internal/auth
PASS; go test ./internal/initdb (112s) + ./cmd/goopg (20s) full pkgs PASS;
make ralph-state-guard (run before status block).
