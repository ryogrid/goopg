(idle — nothing in flight)

## Loop #5 result — M0134-0174 landed

**Nightly triage:** `ci/logs/action-items.md` still at run `20260828-235424`; both
`## AI-` items already filed (001 advisory-lock FIXED, 002 Q5 timeout open). Nothing new.

**Baton check:** tree matched `(idle)` — zero modified `.go` files at start.

**Task:** M0134-0174 `subscription.sql` sized live for the first time
(`not-tried` → **`failed`**, 552 diff lines / 46 `^+ERROR` / 48 `^-ERROR`)
→ **PARKED** at 526 / 29 / 31.

**Shipped (engine-wide silent-acceptance fix):** CREATE SUBSCRIPTION validated
**nothing**. `execCreateSubscription` read two keys out of the `WITH` map
(`enabled`, `slot_name`) and dropped every other name; `Conninfo` went into the
catalog row unread. `CONNECTION 'foo'`, `CONNECTION 'i_dont_exist=param'`,
`WITH (connect=false, enabled=true)`, `WITH (not_an_option=3)` and
`PUBLICATION foo, testpub, foo` all SUCCEEDED. New
`internal/executor/subscription_options.go` ports `parse_subscription_options`
(`subscriptioncmds.c:124`), `check_duplicates_in_publist` (`:2362`) and
`walrcv_check_conninfo`'s `PQconninfoParse` half (`fe-connect.c:6290`). Design
`docs/design/m0134-0174-create-subscription-validation.md`.

**Three things worth carrying:**

1. **The cascade is now a reliable sizing signal, not a nuisance.** 20 of the 46
   divergences were a spurious `subscription already exists`; fixing validation
   collapsed them to 3, and those 3 point *precisely* at the next missing piece
   (the permission checks). When a case reuses one object name across negative
   cases, count the "already exists" errors first — that number is the size of
   the silently-accepted bucket, and the survivors name the next cause.
2. **`specified_opts` is semantics, not bookkeeping.** Upstream distinguishes
   "the user asked for this" from "this is the default" to pick between
   `X and Y are mutually exclusive options` and `subscription with X must also
   set Y`. Four of the eight incompatibility messages are wrong without it — a
   final-value comparison cannot reproduce them.
3. **`alter_generic` is nondeterministic (843 ↔ 841).** The flapping line is
   `catalog update: freshly extended page did not accept tuple`. An A/A on the
   unchanged patched tree reproduced the 843 baseline byte-identically. Same
   class as `plpgsql.sql` — A/A before reading its line count as an A/B signal.

Gates run: `go build ./...` OK; guards `TestParseSubscriptionOptions{Rejects,Accepts}`,
`TestCheckConninfoSyntax`, `TestCheckDuplicatesInPublist`,
`TestCreateSubscriptionRejectedEndToEnd` PASS, **revert-checked at BOTH wiring
points** (removing the `checkConninfoSyntax` call or the `parseSubscriptionOptions`
call fails the end-to-end guard); 8-case regress A/B vs a HEAD worktree
(`subscription` 552→526, **seven byte-identical**, `alter_generic` delta proven
nondeterministic by A/A); `go test ./internal/executor/ ./internal/catalog/
./internal/parser/ ./internal/replication/` PASS;
`RALPH_PRECOMMIT_SCOPE=units scripts/ralph-precommit-test.sh` PASS;
`scripts/tpch-spotcheck.sh` PASS (Q12 rows=2 20.6s, Q13 rows=34 8.2s);
`make regen-testport` + `make check-testport-inventory` PASS.

In-flight: none.

**Carried obligations (19th loop):** TPC-DS SF0.5 gate still NOT run (for -0156, -0157).
-0158..-0174 are parser/DDL/catalog/ACL/wire/type-input/FK/plpgsql/pubsub-only and
cannot move a TPC-DS plan.
