# M0122\-0008 ACL array world\-entry order

Status: **landed 2026\-09\-22** \(the code sits inside commit `a65157d9f`; see
"How this landed" below\). Parent: M0122\-0008.

## The divergence

PostgreSQL's `acldefault` \(`postgres/src/backend/utils/adt/acl.c:804`\)
allocates the PUBLIC \(world\) aclitem **before** the owner's. goopg's shared
ACL renderer injected the owner first, so `datacl`, `typacl` and `proacl`
differed from PostgreSQL as arrays. `aclitem[]` is visible text that
`pg_dump`'s `buildACLCommands` parses, so the order is part of the
compatibility surface, not an internal detail.

## The correction this document records

An earlier draft of this change — and the working\-tree state it left behind —
moved the PUBLIC item to the front of **every** rendered ACL. That is right for
a database, a type and a function, and **wrong for a table**, and the second
half is what makes the rule worth writing down.

`acldefault` writes a world item only for the classes whose `world_default` is
non\-zero:

| class | `world_default` | PUBLIC position |
|---|---|---|
| DATABASE | `ACL_CREATE_TEMP \| ACL_CONNECT` | leads the array |
| FUNCTION | `ACL_EXECUTE` | leads the array |
| LANGUAGE | `ACL_USAGE` | leads the array |
| TYPE / DOMAIN | `ACL_USAGE` | leads the array |
| TABLE, SEQUENCE, SCHEMA, COLUMN, TABLESPACE, LARGE OBJECT, PARAMETER\_ACL, FDW, FOREIGN SERVER | `ACL_NO_RIGHTS` | ordinary grantee, in grant order |

For the second group a PUBLIC entry can only come from an explicit `GRANT`, and
`aclupdate` \(same file\) **appends** a new grantee to the end of the array
without sorting it. So PUBLIC trails there, exactly like any other grantee.

## Measured against the oracle

Captured on a private PostgreSQL 18.3 cluster \(not a reference cluster\),
running the same statements in the same order:

```
table     GRANT SELECT ON t2 TO bob; GRANT SELECT ON t2 TO PUBLIC;
          {postgres=arwdDxtm/postgres,bob=r/postgres,=r/postgres}
database  GRANT CONNECT ON DATABASE d1 TO bob;
          {=Tc/postgres,postgres=CTc/postgres,bob=c/postgres}
```

goopg now returns both byte\-identical, verified end to end on a throwaway
server as well as in unit tests. Before the correction the table arm read
`{=r/postgres,postgres=arwdDxtm/postgres,bob=r/postgres}` — a divergence the
draft would have introduced while fixing another.

## Implementation

`relaclTextLockedFor` takes the class's world\-default property explicitly
\(`aclWorldDefault`\) rather than inferring it from the presence of a PUBLIC
entry; ten call sites pass the constant for their class, and
`DefaultACLText` derives it from the `pg_default_acl` objtype byte \(`'f'` and
`'T'` only\). When the flag is false, PUBLIC stays in the ordinary grant\-order
loops, so it is emitted where it was granted.

`TestACLPublicItemPositionFollowsAcldefault` pins both arms **together**, with
the PG capture quoted in the test: one arm alone cannot distinguish "PUBLIC
leads" from "PUBLIC trails", and a rule that gets either wrong is a
`pg_dump`\-visible break. Non\-vacuity checked — flipping the table class to
has\-world\-default fails exactly that arm. Five expectations the draft had
rewritten are restored to the oracle\-true strings; the type, database and
function ones it corrected are kept, and the stale comment that recorded the
function case as a known divergence is gone, because it no longer is one.

## How this landed \(recorded because the attribution is misleading\)

The code was staged, fully gated and awaiting its commit when a concurrent
owner\-side agent ran a bare `git commit` in the same checkout and swept the
staged files into a commit whose subject was the lineage baseline and said
nothing about ACL rendering. Nothing was lost — all five files landed intact —
and the owner subsequently had that mixed commit split: the code now sits in
`a65157d9f` under its own subject, and this document remains the full record.

The operational lesson is the one the deferral ledger and
`ralph_concurrent_commit_pathspec_required` already state: in this checkout,
commit with an explicit pathspec \(`git commit -F msg -- <paths>`\), never
`git add` followed by a bare `git commit`, because another agent's commit can
consume your index.

## Gates

Catalog and executor unit tests; `tpch-spotcheck` PASS \(Q12=2, Q13=33\);
`tpcds-sf025` sweep `PASS=96 MISMATCH=0 CKMISMATCH=0 ERROR=0 TIMEOUT=0` with
`PLAN-SHAPE same=99 changed=0`; the full `TestPort_RegressSuite`, whose failing
set is unchanged from HEAD \(only `partition_aggregate`, which has its own
`[!]` row\); pgbench smoke.

PostgreSQL reference: `postgres/src/backend/utils/adt/acl.c:804` \(`acldefault`\)
and `aclupdate` in the same file.
