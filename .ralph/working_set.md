(idle — nothing in flight)

## Loop #8 result — M0134-0162 CLOSED GREEN

**Nightly triage:** `ci/logs/action-items.md` still holds only run
`20260828-235424`'s two items; both already filed (fix_plan lines 1538, 1542).
Nothing new to file.

**Task: M0134-0162 (`roleattributes.sql`) — CLOSED, not parked.**
`not-tried` → **`pass`**, 28 → **0** diff lines. First full close in this run of
the M0134 sequence. CSV flipped to `pass`/`pass_required=yes`; `make
regen-testport` + `check-testport-inventory` PASS.

Root cause: `[NO]INHERIT` was accept-and-ignore ENGINE-WIDE —
`catalog.RoleAttrs` had no `Inherit` field, `applyRoleAttrOptions` probed for
its six siblings and never `inherit`, and BOTH pg_authid row builders
hardcoded `rolinherit = 't'` (each with a comment saying so).

**Three things worth carrying:**
1. The fix had a mandatory second half. `rolinherit` is PG's DEFAULT for
   `pg_auth_members.inherit_option` (`user.c:1924-1939`), and goopg's
   `HasPrivsOfRole`/`SelectBestAdmin` traverse ONLY inherit-marked rows.
   Shipping the catalog column without changing `GrantRoleMembership`'s
   hardcoded `true` default would have left a NOINHERIT role inheriting every
   privilege of every role granted to it. `GrantRoleMembership`'s own doc
   comment had stated the "rolinherit is always true" assumption explicitly —
   a comment that names its own invalidation condition is a live tripwire.
2. `rolinherit` is the ONLY pg_authid boolean whose PG default is TRUE, so the
   Go zero value is the wrong seed (same trap as `RoleAttrs.ConnLimit`'s -1).
   Five seed sites need `Inherit: true`; the two `RoleAttrs{}` privilege-check
   literals are "fail closed" sentinels and must NOT be changed.
3. A multi-case regress sequence can mask a real pass. `roleattributes` shows
   364 diff lines when run after `create_role`/`privileges`/`rowsecurity`, but
   0 standalone — the sequenced failure is a distinct pre-existing bug
   (`split: unsupported system btree OID 2676`, filed 0162a).

Gates run: 8-case regress A/B vs HEAD worktree (`create_role`, `dependency`,
`init_privs`, `password`, `privileges`, `rowsecurity`, `security_label`,
`roleattributes`) — all byte-identical after normalising runner tmpfile
headers + the pre-existing nondeterministic Go pointer address in
`rowsecurity`'s policy rendering; ZERO regressions.
`RALPH_PRECOMMIT_SCOPE=units` PASS (EXIT=0). `scripts/tpch-spotcheck.sh` PASS
(Q12 rows=2, Q13 rows=34, private `GOOPG_BIN`). Baseline worktree removed.

In-flight: none.

**Carried obligations (7th loop):**
1. **TPC-DS SF0.5 gate still NOT run** (for -0156, -0157). -0158..-0162 are
   parser/DDL/catalog/role-only and cannot move a TPC-DS plan. Nightly idle:
   `FORCE=1 GOOPG_BIN=$PWD/tmp/goopg-sf05-bin scripts/tpcds-sf05-regression.sh sweep`.
2. **The 110 "presumed stale, pending" 20260827 rows are STILL unadjudicated** —
   nightly testport keeps hitting the 120m timeout with no results.csv
   (`TestPort_IsolationSuite` full-run wedge, playbook §9).

NEXT LOOP (banner is the authority): M-NIGHTLY filing first, then
**M0134-0163 — `rowsecurity.sql`** (status `not-tried`). 0162a-0162c are
backlog follow-ups, not the main sequence.
