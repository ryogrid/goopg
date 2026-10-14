# B-tree Multi-Writer Split Protocol (M0055)

| field      | value |
|------------|-------|
| status     | draft |
| date       | 2026-05-06 |
| supersedes | — |

## 1. Problem

Current split-path structural updates rely on simplified serialization. This limits write scalability and leaves gaps compared to upstream nbtree split-completion lifecycle.

## 2. Goals

- Enable safe concurrent writers for non-conflicting insert paths.
- Provide explicit incomplete-split lifecycle and completion.
- Maintain reader correctness via existing move-right/high-key semantics.

## 3. Protocol

### 3.1 Incomplete split marker

- Introduce explicit page state to mark split not fully propagated upward.
- Writers encountering such state must run split-completion routine before proceeding.

### 3.2 Lock ordering

- Use deterministic sibling/parent lock ordering to avoid deadlocks.
- Keep latch hold times minimal and bounded.

### 3.3 Parent insertion completion

- Ensure parent downlink insertion and marker clear are atomic from protocol perspective.
- Define retry/idempotency rules for crash/recovery and concurrent writer races.

### 3.4 Sibling invariant restoration

- Update both forward and backward sibling metadata where required by protocol.
- Add validation checks in maintenance paths.

## 4. Transition Plan

- Stage 1: add metadata and completion routine while still allowing compatibility fallback.
- Stage 2: remove splitMu as steady-state structural gate.
- Stage 3: enforce completion protocol across all writer paths.

### 4.1 Cross-handle structural gate \(M0122-0010, 2026-09-22\)

The pre-Stage-2 gate now lives in `storage.Pool`, keyed by the full
`RelFileNode`. Every ordinary `Open`, `Create`, and bulk-build BTree handle
obtains the same mutex for one relation. The three existing structural critical
sections \(split insert, deferred split completion, and vacuum unlink\) use
that shared mutex; unrelated relations remain independent.

This closes the correctness hole where each backend's separate BTree instance
had its own `splitMu`, allowing a split on one connection to interleave with a
structural mutation on another. It deliberately does not claim Stage 2:
removing the gate still needs the buffer-pool reader/writer guarantee described
above. `TestOpenedHandlesShareStructuralLock` is the pin: it asserts both handles
receive the SAME mutex and that one holding it blocks the other, and it fails
without the change \("opened handles do not share a relation structural
lock"\).

`TestOpenedHandlesConcurrentlyInsertAcrossSplits` drives 900 distinct keys
through two independently opened handles and verifies every pointer after real
leaf splits, but it is a **functional smoke test, not a race pin**, and the
file now says so. Measured 2026\-09\-22: with `sharedSplitMu` removed from the
constructors it still passes under `-race -count=3`, because its per\-handle
keys ascend and rightmost\-page splits stay consistent under the buffer\-pool
page locks alone.

**Reproducer \(2026\-10\-14\).** `TestStructuralLockInteriorSplitWorkload`
\(`btree\_structural\_race\_test.go`\) is the failing reproducer the gate lacked.
Each of 40 rounds, on a fresh relation:

- 16 handles of one index insert interleaved 200\-byte keys in random order, so
  leaves and then interior pages split while other handles descend through them;
- every key must then be found with its TID;
- a full scan must return every key once, in order;
- the meta page must report level ≥ 2.

`NBTREE\_UNSHARED\_STRUCTURAL\_LOCK=1` gives each handle its own mutex, the state
before the gate. Measured on 16 CPUs:

| configuration | invocations failed |
|---|---|
| per\-handle mutexes | 15 of 15 \(mostly `index out of range \[\-1\]` in `insertIntoBlock`\) |
| shared lock | 0 of 20, plus 3 clean runs under `\-race` |

One 8\-handle round failed only about 1 time in 13, hence the 40 rounds.

**Lock table \(2026\-10\-14\).** `Pool.BTreeStructuralLock` used to keep a map with
one mutex per relation ever opened, never pruned. Pruning it was unsafe, and a
refcount needs a handle close, which goopg's B\-tree handles do not have. If a
holder releases a mutex that has left the map and a reused relfilenode then
hands the next handle a fresh mutex, the gate is defeated. The map is now a
fixed array of 256 cache\-line\-padded mutexes indexed by a hash of the
`RelFileNode`:

- the same relation always maps to the same mutex, through drops and
  relfilenode reuse;
- memory is constant;
- two relations sharing a stripe only wait for each other. Every structural
  critical section \(split insert, deferred split completion, vacuum unlink\)
  touches one tree and takes no other structural lock, so sharing cannot
  deadlock.

`TestBTreeStructuralLockStripes` pins stability, spread \(2000 consecutive OIDs
use ≥ 90 % of the stripes\) and zero allocation. The reproducer stays green:
0/20 invocations fail, plus clean `\-race` runs.

### 4.2 Toward PG's lock\-coupled protocol \(M0122\-0010, 2026\-10\-14\)

Plan of record for removing `lockStructural`. Each gap is a slice, in dependency
order:

1. **\(S, landed\)** Empty or stale `path`: start at the leftmost page of the
   parent level \(`\_bt\_get\_endpoint`\).
2. **\(M, landed\)** Re\-find the parent by downlink \(`\_bt\_getstackbuf`\):
   move right along the parent level, skipping half\-dead and deleted pages,
   until a downlink equals the split child. Otherwise fail with "failed to
   re\-find parent key".
3. **\(S, landed\)** Clear the child's `INCOMPLETE\_SPLIT` inside the parent's
   no\-split insert record, as `\_bt\_insertonpg` does \(slice 2 below\).
4. **\(M, landed\)** Latch in PG's order: the left page stays latched through
   the parent search and insert, so the child is latched before the parent
   \(slice 3 below\).
5. **\(M\)** Finish incomplete splits on contact \(`\_bt\_moveright` for update,
   `\_bt\_stepright`, `\_bt\_getstackbuf`\). `finishSplit` exists but has no
   production caller. This needs gap 4 first.
6. **\(M\)** Atomic root split: hold left, right, the new root and the metapage
   together. Fix `createNewRoot`'s fallback, which inserts into `parentPath\[0\]`
   \(the root\) whatever the level.
7. **\(M\)** Vacuum: refuse to delete a page whose left sibling is flagged
   \(`\_bt\_leftsib\_splitflag`\), and check that recycling is safe for pages a
   stale path can still name.
8. **\(L\)** Remove `lockStructural`; stress under `\-race`.

**Slice 1 \(gaps 1–2\).** `getStackBuf` / `leftmostAtLevel` replace the blind
`path\[len\(path\)\-1\]` at the split's parent step; `TestGetStackBuf` pins them.
The acceptance instrument is `TestStructuralLockInteriorSplitWorkload` with
`NBTREE\_UNSHARED\_STRUCTURAL\_LOCK=1`:

| state | unshared invocations failed | failure classes |
|---|---|---|
| before | 15 / 15 | mostly `index out of range \[\-1\]` \(empty path\) |
| after slice 1 | 5 / 15 | 4 "failed to re\-find parent key" \(gap 5\); 1 "no level 1 \(root level 0\)" \(gap 6\) |

The shared\-lock configuration stays at 0/20 failures, and the package passes under
`\-race`.

**Slice 2 \(gap 3\).** A downlink inserted into an internal page without a split
used to go out as `XLOG\_BTREE\_INSERT\_LEAF`, a PG parity bug of its own. The
child's flag was then cleared by a separate page record, so a crash between the
two left a finished split flagged. `insertUpperFinishChild` now:

- latches the child, parent before child, the split path's existing order;
- clears the child's flag;
- logs one `XLOG\_BTREE\_INSERT\_UPPER` record: block 0 the parent with the item,
  block 1 the child, bare and `SAME\_REL` \(`EncodeBtreeInsertUpperPG`, the
  `LogBtreeInsertUpper` pool hook\);
- marks both pages covered by the record.

The redo arm already existed for PG\-written records.

Evidence:

- the encoder is byte\-identical to the hand\-built PG record;
- replaying goopg's record inserts the downlink and clears the flag;
- `TestInternalInsertFinishesChildSplit`, whose negative control fails;
- `pg\_waldump` decodes 885 `INSERT\_UPPER` records from a 30 000\-row randomised
  index build;
- after `kill \-9` and recovery from a redo point before the build, the index
  agrees with the heap \(30 000 = 30 000, and 310 point lookups all found\).

Unshared reproducer after slice 2: 13/15 failures, up from 5/15, all of the
gap\-5 class \("failed to re\-find parent key"\). There is no new class and no panic
or deadlock. Latching and logging the child lengthens the parent insert, which
widens the window in which another handle's half\-finished split has no downlink
yet; gap 5 closes that window. So the per\-slice acceptance rule is "no new
failure class in the unshared run, 0/20 in the shared run", not a falling count.
Shared: 0/20, `\-race` clean.

**Slice 3 \(gap 4\).** The split no longer releases its left page before going
up a level:

- the left page is handed, still latched, to the parent insert
  \(`insertIntoBlock`'s `heldChild`, transferred with `wlatch.take`\);
- the parent insert releases it once the record that clears its
  `INCOMPLETE\_SPLIT` is written: the `INSERT\_UPPER` record, or the parent's own
  split record;
- the child is therefore always latched before its parent, and no other writer
  can see the half split between the split and the parent insert;
- a root split still releases the left page first, until gap 6 makes the root
  lift atomic;
- `getStackBuf` refuses a path hint naming the held child, which is only possible
  after block reuse, and starts from the level's leftmost page instead.

Unshared reproducer: 3/15 failures, down from 13/15 \(5/15 after slice 1\):

- 1 is the gap\-6 "no level 1 \(root level 0\)";
- 2 are latch deadlocks. In both, one handle's root split had raced a concurrent
  root lift, taken the `createNewRoot` fallback into the new root, and then
  latched the child after that parent: the last parent\-before\-child order, which
  gap 6 removes. The other handle held that child and waited in `getStackBuf`.

A concurrent root lift needs two splitters of one index at once, which the
shared structural lock rules out. Shared: 0/20, and the package passes under
`\-race`.

## 5. Tests

- Multi-writer split stress with random keys.
- Concurrent split on adjacent leaves.
- Crash/restart during split propagation.
- Deadlock and livelock regression tests.

## 6. Acceptance

- No structural corruption under stress/recovery tests.
- Writer throughput scales beyond single structural writer baseline.
- splitMu no longer governs normal structural write flow.
