# M0134-0159 — SQL comments are whitespace: unblocking the compat-tier statement intercepts

**Status:** landed
**Milestone / task:** M0134-0159 (`regproc.sql`, regress-sql `not-tried` → `failed`)
**Area:** `internal/postmaster` (wire dispatch, compat statement classification, plan-cache key)

## Summary

goopg's postmaster carries a *compat tier*: a hand-written classifier for the
statement classes the goyacc grammar deliberately does not implement (role DDL,
database DDL, the `CREATE SCHEMA` header — the playbook §12 list in
`docs/design/not_ralph/06-goyacc-parser-playbook.md`). That classifier
prefix-matches the output of `normalizeCompatSQL`, which lowercased and collapsed
whitespace but **kept comments verbatim**.

PostgreSQL's lexer does not: `scan.l:213-215` defines

```
comment			("--"{non_newline}*)
whitespace		({space}+|{comment})
```

and the `{whitespace}` rule body (`scan.l:443-444`) is `/* ignore */` — a comment
emits no token at all, so a comment can appear anywhere a space can, including in
front of the very first keyword of a statement.

Because goopg kept the comment, `-- setup\nCREATE ROLE r` normalized to
`-- setup create role r`. No `create role ` prefix matched, the statement fell
through to the parser, and the parser — which has no `CREATE ROLE` production —
reported

```
ERROR:  syntax error at or near "expected TABLE, INDEX, VIEW, PUBLICATION,
        SUBSCRIPTION, FUNCTION, PROCEDURE, TRIGGER, EXTENSION, or TABLESPACE
        after CREATE (got role)"
```

**Every one of these statement classes was unreachable from any commented SQL
script**, which is to say from essentially every real one:

| statement | bare | behind a comment (before this fix) |
|---|---|---|
| `CREATE ROLE` / `CREATE USER` / `CREATE GROUP` | works | syntax error |
| `ALTER ROLE` | works | syntax error |
| `DROP ROLE` / `DROP USER` / `DROP GROUP` | works | syntax error |
| `CREATE DATABASE` / `ALTER DATABASE` / `DROP DATABASE` | works | syntax error |

`CREATE TABLE`, `SELECT`, `SET`, `GRANT`, `CREATE SCHEMA` are unaffected — they
have real grammar and never reach the compat tier.

The fix ports the lexer's rule: `normalizeCompatSQL` now runs a
`stripSQLComments` pre-pass that replaces each comment with a single space.

## Discovery

The `regproc.sql` regress case opens with

```sql
/* If objects exist, return oids */

CREATE ROLE regress_regrole_test;
```

which is why this surfaced here. The bug is not regproc-specific — it is engine-wide
and predates the case by a long way.

## Implementation

`internal/postmaster/dispatch.go`

- `stripSQLComments(sql string) string` — a lexical pre-pass, not a rewriter.
  Everything that is not a comment is copied byte for byte.
  - Fast path: a string containing neither `--` nor `/*` is returned unchanged
    with no allocation.
  - A comment introducer **inside a literal is not a comment**, so the scan skips
    the same literal forms `firstTopLevelSemicolon` (`role_ddl.go:1259`) skips —
    single-quoted strings (with `''` doubling), double-quoted identifiers (with
    `""` doubling) and dollar-quoted strings (reusing that file's
    `scanDollarTag`) — **plus** the `E'…'` escape-string form, in which a
    backslash escapes the next byte so `E'a\'-- still literal'` is not cut in
    half. `isIdentByte` distinguishes the `E` prefix from an identifier that
    merely ends in `e` (`type'a'`).
  - Block comments **nest**: `scan.l`'s `<xc>` state carries an `xcdepth`
    counter (`scan.l:455-467`), so `/* a /* b */ c */` is one comment. This is
    a deliberate divergence from `firstTopLevelSemicolon`'s documented
    non-nesting simplification, which is adequate for its own purpose.
  - An **unterminated** comment or literal is copied through verbatim. This
    function must never turn a malformed statement into one the prefix matcher
    accepts; PG raises `unterminated /* comment` (`scan.l:483`) from the lexer
    and goopg's lexer likewise reports it.
- `normalizeCompatSQL` calls it **before** the trailing-`;` trim, so a trailing
  comment can no longer hide the semicolon (`CREATE ROLE r; -- done`).

## Blast radius

`normalizeCompatSQL` has two kinds of caller and both were checked:

1. **Compat statement classification** — `tryHandleRoleDDL`, `splitLeadingRoleDDL`,
   `compatNoopCommandTag`, `registerCompatNoopSchema`, `databaseRenameFromAlter`,
   `tryCompatNoopExtended`. All are prefix/substring matches; none correlates
   indexes between `norm` and the raw `sql` (`extractRolePassword` /
   `extractRoleValidUntil` re-scan the raw `sql` independently), so removing
   bytes from `norm` is safe. `normalizeCompatSQL` already collapsed whitespace,
   so the two were never index-aligned to begin with.
2. **`planCacheKey`** — two statements differing only inside a comment now share
   one cache entry. That is correct: comments are semantically void, so the
   plans are identical. It cannot select a *wrong* plan.

The one path that could in principle have turned a working statement into a
silent no-op is `compatNoopCommandTag`'s absorption. It cannot: that call site
(`dispatch.go:239`) is reached **only after `parser.Parse` has already failed**,
so any statement with real grammar — `GRANT`, `REVOKE`, `COMMENT ON`,
`SECURITY LABEL` — is never absorbed regardless of its comments.

## Verification

- `internal/postmaster/sql_comment_strip_test.go` — 16 `stripSQLComments` cases
  (nesting, all four literal forms, `E''` escapes, unterminated input), 6
  `normalizeCompatSQL` cases pinning the commented-DDL contract, and one
  `splitLeadingRoleDDL` batch-seam case.
- **12-case regress A/B against a HEAD worktree baseline**
  (`regproc create_role roleattributes create_schema privileges init_privs
  database guc namespace create_table select insert`):
  `regproc` **766 → 758** lines, `^+ERROR` **63 → 61**; **ten cases byte-identical**;
  `privileges` apparently grew 4101 → 4111. Run **alone on a fresh server**,
  `privileges` is **byte-identical** (3926 lines / 371 `^+ERROR`) between the two
  builds — the sweep delta was cross-case contamination, not a regression
  (see below).
- `RALPH_PRECOMMIT_SCOPE=units scripts/ralph-precommit-test.sh`; pgbench smoke via
  the pre-commit hook.

### The `privileges` sweep delta — a separate discovery

Under a shared server, `regproc.sql`'s `CREATE ROLE regress_regrole_test` now
actually executes, adding a row to `pg_authid` that later cases inherit. Nine
cases later, `privileges.sql`'s `CREATE USER regress_priv_user9` hits

```
ERROR:  split sys btree 2676: split: unsupported system btree OID 2676
```

OID 2676 is `pg_authid_rolname_index`. **goopg's system B-tree cannot split**, so
role creation hard-fails once `pg_authid`'s index outgrows a single page, and the
failure is not recovered by a later `DROP ROLE` (the slot is not reclaimed). This
change did not cause that ceiling; it moved the run one role closer to it. The
syntax-error inventory of `privileges.sql` is identical between the two builds
(22 errors, same six classes), which is what rules out a semantic regression.
Recorded as its own deferral-ledger row.

## Not done here (see `.ralph/deferral_ledger.md`)

`regproc.sql` remains `failed` at 758 lines. Its dominant remaining buckets are
independent of this fix:

- the entire `to_reg*` soft-error family (`to_regclass`, `to_regoper`,
  `to_regoperator`, `to_regproc`, `to_regprocedure`, `to_regtype`,
  `to_regrole`, `to_regnamespace`, `to_regcollation`, `to_regtypemod`) is
  undispatched — ~35 of the 61 `^+ERROR`s;
- function-style casts `regX('name')` (`expr.go:14296`) are **stubs that echo
  the argument** instead of running the type's input function, so
  `regclass('pg_class')` yields the bare OID `1259` and
  `regproc('pg_catalog.now')` yields the unresolved text `pg_catalog.now`
  where PG yields `pg_class` / `now`. The OID→name half already works
  (`1259::regclass` → `pg_class`, `23::regtype` → `integer`);
- `regoper`/`regoperator` have no name-resolution seam at all;
- `pg_input_error_info` returns an empty row rather than the soft-error fields.
