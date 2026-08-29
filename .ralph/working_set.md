(idle — nothing in flight)

## Loop #8 result — M0134-0176 landed

**Nightly triage:** `ci/logs/action-items.md` unchanged (run `20260828-235424`);
both `## AI-` items already have M-NIGHTLY rows (001 ticked, 002 open). Nothing
new to file.

**Baton check:** previous baton said `(idle)`; selected by ID ascending per the
banner. -0175b..e are filed sub-items, not the next ID.

**Task:** M0134-0176 — tablespace storage parameters. `tablespace.sql`
**854 → 811** diff lines, `^+ERROR` 32 → **25**. Design
`docs/design/m0134-0176-tablespace-storage-parameters.md`.

**Four things worth carrying:**

1. **The *declared but unconsumed* pattern, FOURTH instance** (after
   `client_min_messages`, `fillfactor`, and the relation reloptions of
   M0134-0160). `CREATE TABLESPACE ... WITH (...)` was lexed, grammar-bounded,
   stored on the AST — as a **raw token dump** — and read by nobody. Grep for a
   *consuming* reference, not any reference. `pg_tablespace_location` (filed
   -0176b) is a fifth instance found in the same file: a catalogued `pg_proc`
   OID with no dispatch, returning an empty row instead of erroring.

2. **A live PG 18.3 oracle is cheap and it paid off.** `initdb --auth=trust` +
   `pg_ctl -o "-p 5541"` into /tmp takes ~30s and settled four semantics the
   source alone left ambiguous — most importantly that **`RESET (bogus_never_set)`
   SUCCEEDS** (validation runs on the MERGED array, so a name is only ever
   checked on the way *in*) and that emptying the array gives **NULL, not `{}`**.
   It also surfaced the one divergence that remains: `SET (seq_page_cost)` (bare
   name) stores `=true` where PG rejects the VALUE TYPE — ledgered.

3. **Port the shape, not just the answer.** `RESET (name = value)` → 42601 is
   only *expressible* because the clause is an ordered list with a `HasValue`
   bit. A `map[string]string` (what CREATE TABLE still uses — M0134-0160a) can
   represent neither "no value" nor source order. Upstream's own comment says
   "the grammar doesn't enforce it", which is the tell that the parser must
   RECORD rather than reject.

4. **Check whether a grammar change is even needed.** Tablespace DDL is one of
   the classes the goyacc port deliberately does not carry (playbook §12):
   `CREATE` was already hand-written and `routedCreatePairs["alter"]` has no
   `"tablespace"`, so `ALTER TABLESPACE` never reaches the grammar. The new arm
   sits beside its `CREATE` sibling — no `grammar/*.y` edit, no goldens.

**Two traps hit:** an **orphaned server on 5533** silently answered my probe with
old behavior (`bind: address already in use` was buried in the server log, and
psql connected happily to the orphan) — always grep the server log for it before
believing a "no change" result. And `3` lexes as `TokenIntLit`, not
`TokenNumericLit`; `1.5` is the numeric one.

**Discovered, filed, not fixed:** M0134-0176a (`ALTER ... ALL IN TABLESPACE`
unparsed — the working RENAME unmasked its cascade into the case's final
`DROP TABLESPACE`), M0134-0176b (`pg_tablespace_location` undispatched).

**Gates run:** `RALPH_PRECOMMIT_SCOPE=units scripts/ralph-precommit-test.sh`
PASS; `scripts/tpch-spotcheck.sh` PASS (Q12 rows=2 24.9s, Q13 rows=34 8.8s);
`go test` on internal/{executor,parser,catalog,optimizer,postmaster} PASS;
`scripts/pg-regress-runner.sh tablespace` before/after; live PG 18.3 A/B;
`make regen-testport` + `make check-testport-inventory` PASS;
`make ralph-state-guard` OK (auto-repaired the progress marker). gofmt drift on
ddl.go/ast.go/operators_ddl.go/planner.go/dispatch.go is **pre-existing at HEAD**
(verified by stash) — none of it touches the new lines.

**In-flight:** none.
