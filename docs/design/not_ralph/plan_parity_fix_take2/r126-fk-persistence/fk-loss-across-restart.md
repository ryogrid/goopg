# Measured: FK constraints do not survive a restart — and enforcement goes with them

Captured 2026-09-14 at HEAD `14a11488f` (R125 + addendum), replacing the
prose-only claim R125 §7 flagged as load-bearing and unreproducible.

Throwaway cluster, goopg built from HEAD, `GOOPG_CG_UNIT=r126-fkprobe`,
`-D /tmp/r126fk --listen 127.0.0.1:5533`. Server logs:
`/tmp/r126fk-server1.log` (pre-restart), `/tmp/r126fk-server2.log` (post).

*(A short data-dir path is required: the scratchpad path makes
`<datadir>/.goopg.ctl.sock` exceed the ~108-byte AF_UNIX limit and the
server dies with `control listener: bind: invalid argument` AFTER
binding its TCP port — i.e. it logs "listener bound" and still exits.)*

## Setup

Database `postgres`: `parent(id int PRIMARY KEY, v text)` 100 rows,
`child(id int PRIMARY KEY, pid int, w text, CONSTRAINT child_pid_fkey
FOREIGN KEY (pid) REFERENCES parent(id))` 500 rows.
Database `fkdb` (`CREATE DATABASE`): `dparent`/`dchild` with
`dchild_pid_fkey`, 50 parent rows.

## Before the restart

```
    conname     | contype |  rel  | refrel | conkey | confkey | convalidated | conenforced
----------------+---------+-------+--------+--------+---------+--------------+-------------
 child_pid_fkey | f       | child | parent | {2}    | {1}     | t            | t
```
`fkdb` likewise shows `dchild_pid_fkey | dchild | dparent`.

Note the synthesised view **does** render `conkey={2}` / `confkey={1}`,
because it builds them as text (`catalog.go:7270-7280`) — this is NOT
evidence that the heap array encoding works. It does not; see SCOPE §3/Q1.

Then `CHECKPOINT`, `goopg stop` (clean: "server stopped"), restart.

## After the restart

| probe | result |
|---|---|
| `SELECT … FROM pg_constraint WHERE contype='f'` (db `postgres`) | **0 rows** |
| same, db `fkdb` | **0 rows** |
| `SELECT count(*) FROM pg_constraint` | **0** — the view is entirely empty |
| `SELECT count(*) FROM child` | 500 — **data intact** |
| `SELECT count(*) FROM dparent` | 50 — intact |
| indexes on parent/child | `parent_pkey`, `child_pkey` — **survive** |

## The loss is not confined to planner evidence — it is silent data corruption

```
INSERT INTO child VALUES (99999, 99999, 'orphan');   -- pid=99999 has no parent
INSERT 0 1
SELECT count(*) FROM child WHERE pid=99999;  -->  1
```

**The orphan row is accepted.** Referential integrity is not merely
un-*planned* after a restart, it is un-*enforced*, silently and with no
warning. Runtime FK enforcement reads `catalog.Table.ForeignKeys`
(`operators_fk.go:118`, `:170`), the same field the reload never
repopulates.

By contrast PK uniqueness **is** still enforced, because it rides the
index rather than the constraint catalog:

```
INSERT INTO parent VALUES (1,'dup');
ERROR:  duplicate key value violates unique constraint "parent_pkey"
```

## Two findings, only one of which R126 fixes

1. **FK rows are never written to the pg_constraint heap and never
   reloaded** — R126's subject.
2. **`pg_constraint` returns 0 rows even for PRIMARY KEY / UNIQUE**,
   whose `contype='p'/'u'` rows the view synthesises from indexes
   (`catalog.go:7143-7153`) — and the indexes demonstrably survived. So a
   *second*, independent reload gap suppresses the PK projection.
   **Out of R126's scope; it must not be conflated with finding 1**, and
   a successor round should chase it (likely the index's
   primary/unique-constraint marking rather than the index itself).

## Bearing on R126

- Confirms the round's premise, and confirms it in a **non-`postgres`
  database** as well — the case SCOPE P1b exists for.
- Raises the round's stakes past plan parity: this is a correctness bug.
- Supplies the before-half of R126's own P1: after the fix, every row of
  the post-restart table above must invert, **including the orphan
  insert now failing**.
