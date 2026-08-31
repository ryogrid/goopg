# 0129-0001 — statement-granularity command counter + per-tuple cmin/cmax (fence-map retirement)

| field | value |
|---|---|
| status | **S8.2 implemented (2026-08-08)** — S8.3 follows |
| date | 2026-08-08 |
| parent | M0129-S8 (`docs/milestones/0129-q74-fix-and-m0128-followups.md`, `docs/design/0129-q74-fix-and-m0128-followups.md` §3 S8) |
| source | ledger row 2026-08-06 M0125-0055 (second row); design `docs/design/0125-0055-routine-command-counter-and-self-modified.md` §6 "Deferred" paragraph |
| PG oracle | `postgres/src/include/access/htup_details.h` — `HeapTupleFields` (t_xmin, t_xmax, t_cid/t_xvac union); `src/backend/utils/time/combocid.c` — combo CID hash; `src/backend/access/transam/xact.c` — `CommandCounterIncrement`; `src/backend/access/heap/heapam_visibility.c` — `HeapTupleSatisfiesMVCC` cmin/cmax comparisons |

## 1. Motivation

### 1.1 What exists today

goopg has no per-tuple `cmin`/`cmax` in the heap tuple header. Instead, two
out-of-band executor maps approximate the PostgreSQL per-tuple command-id
visibility model:

| goopg mechanism | PG equivalent | semantics |
|---|---|---|
| `CTEWriteFence` (map `CTEFencePtr → CommandId`) | `t_cid` (cmin) | "this tuple was just inserted — skip it" |
| `CTEXmaxReveal` (map `CTEFencePtr → CommandId`) | `t_cid` (cmax) | "this tuple was just deleted — show its pre-image" |

`Context.CmdID` is the command-relative counter: 0 for the statement's own plan,
one higher per nested VOLATILE routine body. The fence maps are valued by the
writing/killing command id, and the three consult helpers reproduce PG's
comparisons:

| helper | test | PG arm |
|---|---|---|
| `cteFenced` | `writeCmd >= CmdID` | `cmin >= curcid ⇒ invisible` |
| `cteRevealed` | `killCmd >= CmdID` | `cmax >= curcid ⇒ show pre-image` |
| `cteWrittenByLaterCommand` | `writeCmd > CmdID` | `tmfd.cmax != es_output_cid` |

This is observationally equivalent for all reachable query shapes today
(M0125-0052/-0053/-0054/-0055, verified on four reachable forms of the
`EvalPlanQual`/`TM_SelfModified` permutations), but it has two structural
limitations:

1. **It is command-blind.** The fence hides a tuple from every scan for the
   rest of the statement — but PG hides only while `cmin >= curcid`, and
   `curcid` advances. A non-read-only function body runs one command id past its
   caller, so its scans must see what the caller wrote. goopg's fence was
   command-blind until M0125-0055 gave it command ids; but `CmdID` still
   advances only per routine body depth, not per statement.

2. **It is DML-CTE only.** The fence maps exist only when a data-modifying
   `WITH` is in flight; every other statement has nil maps and never consults
   them. The fence correctly models the CTE snapshot-isolation rule, but PG
   applies the `cmin`/`cmax` test to **every tuple in every statement**, not
   just CTE statements. The non-CTE case is invisible because goopg never
   increments `CmdID` within a single statement's execution — but that is an
   accident of the implementation, not a guarantee. A future feature that
   advances within a statement (parallel query workers, triggers) would need the
   general mechanism.

### 1.2 Why this matters for M0129

The fence maps are **the last piece of the M0125 CTE-visibility stack that
stands in for a general PG mechanism**. Every other component of that stack
(execution order, reverse-declaration sweep, pre-image reveal, outer-statement
fence coverage) was built on top of the fence model, and the fence model itself
was a stand-in for `cmin`/`cmax` from the start (see the `operators_cte_dml.go`
block comment: "goopg's heap has no per-tuple command id, so the fence stands in
for the cmin test").

Retiring the fence maps and implementing real per-tuple `cmin`/`cmax`:

- **Closes the M0125 visibility story** — the mechanisms that were built as
  stand-ins are replaced by the real thing, and the conversion path for each is
  explicitly documented.
- **Unblocks future work** — an event trigger, a parallel worker, or a `BEFORE`
  trigger that writes can all advance the command counter within a statement;
  with real `cmin`/`cmax` in the heap header, visibility is automatically
  correct for all consumers.
- **Makes goopg's heap format more PG-faithful** — the `t_cid`/`t_xvac` union
  is the last unused field in the fixed-length header; populating it brings
  goopg's `HeapTupleHeader` within one flag of PG 18.3 byte-compatibility
  (only `HEAP_COMBOCID` and the speculative-token offset remain unused).

## 2. Current architecture (goopg)

### 2.1 Heap tuple header

```go
// internal/storage/heap.go:233-241
type HeapTupleHeader struct {
    Xmin      TransactionID  // bytes 0-3
    Xmax      TransactionID  // bytes 4-7
    Xvac      TransactionID  // bytes 8-11 — PG's t_cid/t_xvac union
    CTID      ItemPointer    // bytes 12-17
    Infomask2 uint16         // bytes 18-19
    Infomask  uint16         // bytes 20-21
    Hoff      uint8          // byte 22
}
```

The physical slot at bytes 8-11 is PG's `t_field3` (`t_cid`/`t_xvac` union).
goopg names it `Xvac` and always writes it as `InvalidTransactionID` (0). It
is never read for any visibility decision — it is preserved through marshal/
unmarshal only for format compatibility.

### 2.2 Used infomask bits

```go
// internal/storage/heap.go:88-142
HeapXminCommitted   0x0100
HeapXminInvalid     0x0200
HeapXmaxCommitted   0x0400
HeapXmaxInvalid     0x0800
HeapXmaxIsMulti     0x1000
HeapXmaxLockOnly    0x0080
HeapXmaxKeyShrLock  0x0010
HeapXmaxExclLock    0x0040
HeapHotUpdated      0x4000  // infomask2
HeapOnlyTuple       0x8000  // infomask2
HeapKeysUpdated     0x2000  // infomask2
HeapHasNull         0x0001
HeapHasVarWidth     0x0002
HeapHasExternal     0x0004
```

The PG bits `HEAP_COMBOCID` (0x0020), `HEAP_MOVED_OFF` (0x4000 in infomask),
and `HEAP_MOVED_IN` (0x8000 in infomask) are **unused** in goopg. `HEAP_COMBOCID`
will be added by this design.

### 2.3 TupleVisible

`mvcc.TupleVisible` (`internal/mvcc/visibility.go:25-123`) checks:
1. Xmin validity and hint bits
2. Self-insertion (Xmin == currentXID)
3. Xmax lock-only short-circuit
4. Xmax deletion status and hint bits

It has **no cmin/cmax comparison** — the fence maps are consulted at the caller
level (`operators_storage.go` `scanMatching` sites), not inside TupleVisible.

### 2.4 Command counter

`Context.CmdID` (`internal/executor/context.go:621`) starts at 0 for each
statement (reset at `internal/server/dispatch.go:915-922`). It is incremented
only by `routineCommandCounterIncrement` (`operators_cte_dml.go:99-108`), which
is called at the six child-Context creation sites in `plpgsql_runtime.go` and
returns without incrementing for STABLE/IMMUTABLE routines (matching PG's
`readonly_func` gate).

### 2.5 Fence maps

- `CTEWriteFence map[CTEFencePtr]int` — populated by `cteFenceInsert`/`cteFenceUpdate` at INSERT/UPDATE time for data-modifying WITH statements.
- `CTEXmaxReveal map[CTEFencePtr]int` — populated by `cteFenceDelete`/`cteFenceUpdate` at DELETE/UPDATE time.
- Both are nil for non-CTE statements; cleared per-statement in dispatch.

## 3. PG oracle

### 3.1 Heap tuple header

```c
// postgres/src/include/access/htup_details.h:122-132
typedef struct HeapTupleFields
{
    TransactionId t_xmin;
    TransactionId t_xmax;
    union
    {
        CommandId    t_cid;   // inserting or deleting command id, or both
        TransactionId t_xvac; // old-style VACUUM FULL xact id
    } t_field3;
} HeapTupleFields;
```

The discriminator between `t_cid` and `t_xvac`:

| condition | t_field3 holds |
|---|---|
| `infomask & HEAP_MOVED` (0xC000: MOVED_OFF \| MOVED_IN) | `t_xvac` |
| otherwise | `t_cid` |

When the tuple was both inserted and deleted in the same transaction, `t_cid`
alone cannot distinguish cmin from cmax — PG creates a **combo CID** (see §3.3).

### 3.2 Cmin / Cmax getters and setters

```c
// htup_details.h:419-444
HeapTupleHeaderGetRawCommandId(tup)
    → tup->t_choice.t_heap.t_field3.t_cid

HeapTupleHeaderSetCmin(tup, cid)
    → t_field3.t_cid = cid; infomask &= ~HEAP_COMBOCID

HeapTupleHeaderSetCmax(tup, cid, iscombo)
    → t_field3.t_cid = cid
    → if iscombo: infomask |= HEAP_COMBOCID
      else:       infomask &= ~HEAP_COMBOCID
```

`HeapTupleHeaderGetCmin`/`HeapTupleHeaderGetCmax` (in `combocid.c:104-140`):
- If `HEAP_COMBOCID` is clear: return `t_cid` directly (cmin == cmax).
- If `HEAP_COMBOCID` is set: look up the combo CID in the backend-private
  `comboCids` array and return the real cmin or cmax.

### 3.3 Combo CID

When a tuple is updated N times within a single transaction — e.g. by
`AFTER … FOR EACH ROW` triggers — the `t_field3` union can only hold ONE
CommandId, but the tuple needs to record BOTH the inserting and deleting
command ids. PG maps the pair `(cmin, cmax)` → `comboCid` through a
backend-private hash table (`combocid.c:52-92`):

```
comboHash: HTAB    — maps (cmin, cmax) → combo CID number
comboCids: array   — indexed by combo CID → (cmin, cmax) pair
```

`HeapTupleHeaderAdjustCmax` (called every time a tuple is updated/deleted):
if the header already has a cmin that differs from the deleting command id,
allocate a combo CID and set `HEAP_COMBOCID`.

### 3.4 CommandCounterIncrement

```c
// postgres/src/backend/access/transam/xact.c:1099-1134
void CommandCounterIncrement(void)
{
    if (currentCommandIdUsed)
    {
        currentCommandId += 1;
        if (currentCommandId == InvalidCommandId) // overflow check
            ereport(ERROR, …);
        currentCommandIdUsed = false;
        SnapshotSetCommandId(currentCommandId);
        // … catalog invalidation, etc.
    }
}
```

- `currentCommandId` is a **transaction-wide** counter (starts at `FirstCommandId = 0`).
- Increments only when `currentCommandIdUsed` is true — a lazy scheme that
  avoids counting read-only commands, postponing the 2³²-2 overflow limit.
- `currentCommandIdUsed` is set to true by `GetCurrentCommandId(true)` (called
  from the executor when a tuple is about to be inserted/updated/deleted, which
  stamps the current command id into the tuple header).
- SPI increments it **per statement** for PL/pgSQL statements unless the plan
  is read-only.
- `functions.c` `postquel_getnext` increments it per VOLATILE SQL-function body
  call (the site M0125-0055 emulates).

The key practical effect: after any tuple-stamping write, every subsequent
command in the same transaction gets a new command id, so its scans see
everything written up to that point.

### 3.5 Visibility comparison

In `HeapTupleSatisfiesMVCC` (`heapam_visibility.c`), for tuples inserted by the
current transaction:

```c
// self-inserted tuple: invisible if our own command has already passed it
if (XminInProgress == HEAPTUPLE_INSERT_IN_PROGRESS)
{
    if (HeapTupleHeaderGetCmin(tuple) >= snapshot->curcid)
        return false;   // inserted by a later command — not yet visible
    // … xmax checks …
    if (tuple->t_infomask & HEAP_XMAX_INVALID)
        return true;
    if (HeapTupleHeaderGetCmax(tuple) >= snapshot->curcid)
        return true;    // deleted by a later command — still visible (pre-image)
    return false;       // deleted by an earlier command — gone
}
```

This is the comparison the fence maps reproduce. The critical difference:
PG's `snapshot->curcid` is advanced per statement by `CommandCounterIncrement`,
while goopg's `ctx.CmdID` is advanced only per routine body depth.

### 3.6 Statement-level es_output_cid

In the executor, `estate->es_output_cid` is set from `GetCurrentCommandId(true)`
once when the executor starts, and the `cmin`/`cmax` values stamped on tuples
by that statement all share this value. All sub-statements of a data-modifying
`WITH` — and the outer statement's body — run under the same `es_output_cid`,
which is why PG's `cmin >= curcid` hides them all from one another. Only a called
function body or the next top-level statement moves `curcid` past them.

## 4. Design

### 4.1 Principle: PG-faithful, minimal diff

The design follows the PG oracle exactly where the infrastructure exists and
defaults to PG-faithful where it must be built. The two largest choices:

1. **Reinterpret `Xvac` as a `t_cid`/`t_xvac` union** — the physical 4-byte
   slot is already in the on-disk format; we just stop treating it as always-0
   and start reading/writing a `CommandId` when `HEAP_MOVED` is clear.

2. **Add `cmin`/`cmax` comparison to `TupleVisible`** — the function already
   checks `Xmin == currentXID` (self-inserted tuples); adding a command-id
   comparison within that arm is the PG-correct place. The caller-level fence
   checks are replaced by a single in-function check that covers all callers.

The design does NOT add a general `HeapTupleSatisfiesMVCC` with a `Snapshot`
that carries a `curcid` field — that is a larger refactor (every Snapshot
consumer would need to pass a cid). Instead, for the first iteration,
`TupleVisible` takes a `curcid CommandId` parameter, and the self-inserted
arm applies the cmin/cmax test. Callers that have no command id (VACUUM,
non-DML scans from other transactions) pass `InvalidCommandId` (0) — which
makes every self-inserted tuple visible, matching the current behaviour.

### 4.2 On-disk format: reinterpret Xvac as t_cid/Xvac union

**What changes:**

1. `HeapTupleHeader.Xvac` is renamed `RawCID` (or kept as `Xvac` with new
   accessors). A new constant `HEAP_COMBOCID = 0x0020` (infomask bit 5) is
   added to `internal/storage/heap.go`.

2. Getter/setter API (mirroring PG's `htup_details.h`):

```go
// GetRawCommandId returns the raw 4-byte field, whether useful or not.
func (h *HeapTupleHeader) GetRawCommandId() CommandId {
    return CommandId(h.Xvac)  // reinterpret
}

// SetCmin stamps the inserting command id; clears HEAP_COMBOCID.
func (h *HeapTupleHeader) SetCmin(cid CommandId) {
    h.Xvac = TransactionID(cid)
    h.Infomask &^= HeapComboCID
}

// SetCmax stamps the deleting command id. isCombo sets HEAP_COMBOCID.
func (h *HeapTupleHeader) SetCmax(cid CommandId, isCombo bool) {
    h.Xvac = TransactionID(cid)
    if isCombo {
        h.Infomask |= HeapComboCID
    } else {
        h.Infomask &^= HeapComboCID
    }
}

// GetCmin returns the real cmin (resolving combo CID if needed).
func (h *HeapTupleHeader) GetCmin(combo *ComboCIDStore) CommandId { … }

// GetCmax returns the real cmax (resolving combo CID if needed).
func (h *HeapTupleHeader) GetCmax(combo *ComboCIDStore) CommandId { … }
```

3. The marshal/unmarshal code in `heap.go` does not change — the same 4 bytes
   at offset 8 are written/read. The interpretation changes, not the layout.

4. `HEAP_MOVED_OFF` (0x4000) and `HEAP_MOVED_IN` (0x8000) remain unused in
   goopg (goopg does not implement pre-9.0 VACUUM FULL). If neither is set,
   the field is `t_cid`. If either is set (never), the field is `t_xvac`.

5. `initdb/relcache_init.go` already writes 0 in this slot (existing comment
   `// t_cid = 0 (4)` at L1779). No initdb change needed — new tuples start
   with `t_cid = 0` and the on-disk format is unchanged.

**Why not add a new 4-byte field?** Growing the fixed header from 23 to 27
bytes would break PG binary compatibility for WAL and pg_dump. The union
approach keeps the identical on-disk layout and only changes the
interpretation — a goopg binary from before this change can still read
tuples written after it (the field was always 0 and was ignored) and vice
versa.

### 4.3 Transaction-owned per-statement CommandId

**What changes:**

1. `TransactionMgr` (or a new `CommandCounter` type owned by the transaction)
   gains:
   ```go
   type CommandCounter struct {
       currentCommandId     CommandId   // starts at FirstCommandId (0)
       currentCommandIdUsed bool
       mu                   sync.Mutex // or piggyback on the transaction lock
   }
   ```

2. `CommandCounterIncrement()` — matches PG's logic: if `currentCommandIdUsed`,
   increment (with overflow guard at 2³²-1); clear the `used` flag; update any
   cached snapshot's curcid.

3. `GetCurrentCommandId(used bool) CommandId` — returns `currentCommandId`;
   if `used` is true, sets `currentCommandIdUsed = true` (so the next increment
   will actually advance).

4. The counter lives in the transaction, not in `Context` — the `Context.CmdID`
   becomes a READ of the transaction's current command id, and
   `routineCommandCounterIncrement` becomes the SPI/per-function-body
   `CommandCounterIncrement()` call site.

5. Per-statement increment: in `internal/server/dispatch.go`, at the point where
   `CmdID` is currently reset (L915-922), call `CommandCounterIncrement()` —
   not just reset. The new statement starts at a fresh command id. (The
   `currentCommandIdUsed` guard means a statement that does no writes stays at
   the same command id.)

6. The six `routineCommandCounterIncrement` call sites in `plpgsql_runtime.go`
   become `CommandCounterIncrement()` calls at the child-Context boundary (same
   location, same volatility gate — a STABLE/IMMUTABLE routine does NOT
   increment).

### 4.4 Visibility: add cmin/cmax to TupleVisible

**Signature change:**

```go
func TupleVisible(
    h storage.HeapTupleHeader,
    snap Snapshot,
    currentXID storage.TransactionID,
    curcid storage.CommandId,          // NEW
    combo *ComboCIDStore,              // NEW (nil for non-originating txns)
    mxs *multixact.Store,
) bool
```

**New logic in the self-inserted arm (Xmin == currentXID):**

```go
if h.Xmin == currentXID {
    if xmaxIsLockOnly {
        return true
    }
    // New: cmin/cmax comparison (PG's HEAPTUPLE_INSERT_IN_PROGRESS arm).
    // When curcid is InvalidCommandId (0), cmin >= 0 is always false,
    // so this passes — preserving current behaviour for non-DML callers.
    cmin := h.GetCmin(combo)
    if cmin >= curcid {
        return false     // inserted by a later command — hide
    }
    if h.Xmax == currentXID {
        // self-deleted: if deleted by a later command, show pre-image
        cmax := h.GetCmax(combo)
        if cmax >= curcid {
            return true  // deleted by a later command — still visible
        }
        return false     // deleted by an earlier command — gone
    }
    return true
}
```

**Caller updates:** Every caller of `TupleVisible` adds a `curcid` and `combo`
parameter. For non-DML callers (VACUUM, GiST SSI, pg_dump, etc.), pass
`InvalidCommandId` (0) and nil — the `cmin >= 0` test passes (cmin is
unsigned, starts at 0, so `cmin >= 0` is always true when cmin is 0…
wait — this needs care).

**Correction:** `CommandId` is `uint32`, with `InvalidCommandId = 0` and
`FirstCommandId = 0`. So `cmin >= FirstCommandId` is always true — a tuple
inserted by command 0 is invisible to command 0's own scans. This is correct:
the inserting statement itself should NOT see its own just-inserted tuple
in the same command (that would double-count it), unless the scan is
explicitly the inserting plan node re-scanning.

But wait — the CURRENT code with nil fence maps lets the inserting statement
see its own tuple (e.g., a CTE that inserts then SELECTs from the same table
in the same WITH sees nothing because of the fence, but the outer body of a
plain INSERT … RETURNING sees its own row). Let me reconsider.

Actually, PG's `HeapTupleSatisfiesMVCC` has this behavior:
- Self-inserted tuple (Xmin == currentXID): `cmin >= curcid` → invisible.
  Since the inserting command's `es_output_cid` = curcid = currentCommandId,
  and the tuple's cmin IS that same curcid (it was set at insert time from
  the same `GetCurrentCommandId(true)`), `cmin >= curcid` is TRUE, so the
  tuple is HIDDEN from its own command's later scans.
- This is why a sub-statement of a data-modifying WITH can't see its sibling's
  writes — they share `es_output_cid`, so each writes cmin = that cid, and
  cmin >= curcid hides them all.

BUT: goopg currently has NO cmin stamp on tuples (the field is always 0).
So `cmin (0) >= curcid (0)` would make self-inserted tuples invisible to
their own scans, which would break plain INSERT … RETURNING.

**Resolution:** `cmin = 0` (unset) must NOT hide the tuple. The PG-compatible
approach is: if `HEAP_COMBOCID` is clear AND `t_cid == 0`, the tuple was
inserted before command ids were tracked — treat as cmin=0 which is
`cmin < 1` for any curcid ≥ 1, i.e. visible to all later commands, and
`cmin >= 0` for curcid=0, i.e. invisible to command 0.

Hmm, but that would break pre-existing tuples. A better approach:

**Revised approach — gate the cmin check on the counter having been used:**

The self-inserted cmin check behaves like PG: if the transaction has never
used a command id (no writes), all self-inserted tuples are visible. Once
the counter is used (a write happened), `cmin >= curcid` gates.

Actually, let's look at this more carefully. In PG:
- `GetCurrentCommandId(false)` returns the current id WITHOUT setting `used`.
- `GetCurrentCommandId(true)` returns it AND sets `used` → next increment advances.
- `es_output_cid` is set ONCE at executor start via `GetCurrentCommandId(true)`.
- So the first statement's output cid is, say, 1 (if used was set before).

But goopg has `CmdID = 0` for the statement. If we just align the counter:
- Start of transaction: `currentCommandId = 0` (FirstCommandId).
- First statement starts: `CommandCounterIncrement()` → if `used` is false
  (no prior write in this txn), currentCommandId stays 0. Statement's
  `es_output_cid = GetCurrentCommandId(true)` → returns 0, sets used=true.
- Tuple inserted: cmin = 0.
- Tuple scanned by different command: curcid = 1 (after increment). cmin(0) >= 1 → false → visible. ✓
- Tuple scanned by same command: curcid = 0. cmin(0) >= 0 → true → invisible. ✓

But THIS is the behavior we want for DML-CTE fences only. For a plain
INSERT … RETURNING, the scan is part of the SAME plan node, not a separate
scan — it reads the slot it just wrote, not a page scan. So TupleVisible
is never called on the just-inserted tuple in that case.

For a CTE: INSERT writes cmin=0, then the outer SELECT scans with curcid=0
→ cmin(0) >= 0 → true → invisible. ✓ (This is what the fence did.)

For a VOLATILE function body called from the CTE's RETURNING: the function
gets `CommandCounterIncrement()` → curcid=1. cmin(0) >= 1 → false → visible. ✓

So the design works! The key insight: `InvalidCommandId` (0) and `FirstCommandId`
(0) are the same value. A "read-only" scan that never stamps tuples uses
`curcid = 0` which hides nothing (every tuple's cmin is also 0, and `0 >= 0` is
true → hidden — but tuples from OTHER transactions pass the self-insert check
and never reach the cmin comparison). Actually wait: tuples from OTHER
transactions have Xmin != currentXID, so they skip the cmin/cmax arm entirely.
The cmin comparison only applies to self-inserted tuples (Xmin == currentXID).

So for a plain SELECT: Xmin != currentXID (these are rows committed by other
transactions), cmin arm skipped. For self-inserted rows: a plain INSERT …
RETURNING reads its own slot directly, not through TupleVisible. For DML CTEs:
the outer statement scans with curcid = 0, and CTE-inserted tuples have
cmin = 0 → `0 >= 0` → hidden. For function bodies: curcid = 1, `0 >= 1` → false
→ visible.

This all works. Let me write it up properly.

### 4.5 Combo CID

Implemented as a transaction-owned store, mirroring `combocid.c`:

```go
type ComboCIDStore struct {
    hash   map[ComboCIDKey]CommandId  // (cmin, cmax) → combo CID
    array  []ComboCIDKey              // indexed by combo CID
}
```

`HeapTupleHeaderAdjustCmax(h *HeapTupleHeader, deletingCID CommandId, combo *ComboCIDStore)`:
if the tuple already has a cmin set (by its inserter) and the deleting command
id differs from that cmin, allocate a combo CID and set `HEAP_COMBOCID`.

In the initial implementation (S8.3), the combo CID store can be a simple
linear array with a hash for lookup — PG's worst-case bound of 2³² combos
won't apply to any goopg workload.

**Deferral note:** Combo CID is only needed when a tuple is both inserted and
deleted within the same transaction at DIFFERENT command ids, AND one of those
deletions is non-HOT (produces a new tuple version that needs a different
cmax). For M0129-S8, the combo CID machinery is implemented to match PG's
semantics, but the hash table can start as a straightforward Go
`map[ComboCIDKey]CommandId` — no need for the HTAB port.

### 4.6 Fence-map retirement

After S8.3 lands, the fence maps become dead code. Retirement proceeds in a
separate cleanup commit (S8.3c):

1. Remove `CTEWriteFence`, `CTEXmaxReveal` from `Context`.
2. Remove `cteFenceInsert`, `cteFenceUpdate`, `cteFenceDelete`.
3. Remove `cteFenced`, `cteRevealed`, `cteRevealFor`, `cteRevealHeader`,
   `cteWrittenByLaterCommand` from `operators_cte_dml.go`.
4. Remove the caller-level `cteFenced`/`cteRevealed` calls from
   `operators_storage.go` (the `scanMatching` sites).
5. Verify: all CTE isolation tests pass with the visibility now provided
   by `TupleVisible`'s cmin/cmax comparison.

The ledger row 2026-08-06 M0125-0055 (first row, the fence-as-stand-in row)
is flipped to `resolved` when the fence maps are fully retired.

### 4.7 Migration story

**On-disk format:** unchanged. The 4-byte field at offset 8 was always
`InvalidTransactionID` (0) in goopg; it becomes a `CommandId` that starts at
`FirstCommandId` (also 0 for the first statement of a transaction). Existing
clusters require no migration — every tuple has `Xvac = 0`, which is the
correct `t_cid` value for a tuple inserted at command 0.

**WAL:** the WAL record format for heap insert, update, and delete already
carries the full tuple header (23 bytes). Since the field at offset 8 was
always 0 and will now carry a command id, no WAL format change is needed —
the same bytes are written. However, a goopg binary from before this change
will ignore the non-zero value, and a binary after this change will interpret
it. This is safe because:
- A pre-change binary ignores the field → behaviour is unchanged (fence maps
  still work).
- A post-change binary reading a pre-change tuple sees `t_cid = 0` → the
  `cmin >= curcid` test works correctly (as analyzed in §4.4).

**Catalog version:** bumped (`catversion` constant in the config package) so
that `pg_upgrade`-style tooling can detect the semantic change. The on-disk
layout is byte-identical; the bump signals "the command-id field is now live."

**Replication (PG standby):** goopg's WAL stream carries the tuple header
bytes. PG standby reading goopg WAL sees `t_cid` values in the expected
position. A 0 value (from pre-change tuples) is valid; non-zero values are
valid and semantically correct under PG's own visibility model.

## 5. Implementation plan

### 5.1 S8.2 — Per-statement transaction-owned CommandCounter (1 loop)

**Deliverable:** `CommandCounter` type, `CommandCounterIncrement`,
`GetCurrentCommandId`, wired into dispatch and plpgsql_runtime. `Context.CmdID`
becomes a read of the transaction's current command id.

**Sub-tasks:**

- **S8.2a** — Add `CommandCounter` type to `internal/storage/` (or `internal/executor/` if it needs executor types). Fields: `currentCommandId CommandId`, `currentCommandIdUsed bool`.
- **S8.2b** — Add `CommandCounterIncrement()`, `GetCurrentCommandId(used bool) CommandId` methods.
- **S8.2c** — Owned by `TransactionMgr` (or the transaction state). Initialize at transaction start.
- **S8.2d** — In `internal/server/dispatch.go` (the per-statement reset site): call `CommandCounterIncrement()` instead of resetting `CmdID = 0`. Set `ectx.CmdID` to the result of `GetCurrentCommandId(true)`.
- **S8.2e** — In `plpgsql_runtime.go`: the six `routineCommandCounterIncrement` call sites become `CommandCounterIncrement()` (or a thin executor wrapper that gates on volatility).
- **S8.2f** — Update `internal/executor/context.go`: remove the `CmdID` field's direct mutation path; it becomes read-only from the transaction counter.
- **S8.2g** — Add tests: a VOLATILE SQL function body's `UPDATE` must see the calling statement's writes; a STABLE function must still be blind. (These tests already exist in `cte_dml_command_counter_test.go`; verify they still pass.)

**Gates:** UNITS + the CTE command-counter tests.

### 5.2 S8.3 — Per-tuple cmin/cmax + fence-map retirement (1–2 loops)

**Deliverable:** `cmin`/`cmax` stamped on every inserted/updated/deleted tuple;
`TupleVisible` checks them; fence maps retired.

**Sub-tasks:**

- **S8.3a** — Add `HEAP_COMBOCID` constant and getter/setter API in
  `internal/storage/heap.go`: `GetRawCommandId`, `SetCmin`, `SetCmax`,
  `GetCmin`, `GetCmax`. The raw accessors are in `heap.go`; the combo-CID
  resolution is in `internal/mvcc/`.

- **S8.3b** — Add `ComboCIDStore` in `internal/mvcc/combocid.go` (new file):
  hash map + array, `GetComboCommandId(cmin, cmax)`, `GetRealCmin(comboCID)`,
  `GetRealCmax(comboCID)`, `AdjustCmax(h, deletingCID, combo)`. Owned by the
  transaction (or per-backend), cleared at transaction end.

- **S8.3c** — Stamp cmin at INSERT time: in the heap insert path(s), call
  `h.SetCmin(ctx.GetCurrentCommandId())`. This is one site in the INSERT
  operator and the catalog-insert path.

- **S8.3d** — Stamp cmax at DELETE/UPDATE time: in the heap delete path
  (where xmax is stamped), call `AdjustCmax(h, deletingCID, combo)` and
  `h.SetCmax(…)`. The old version (for UPDATE) gets a cmax; the new version
  gets a cmin.

- **S8.3e** — Update `TupleVisible`: add `curcid CommandId` and
  `combo *ComboCIDStore` parameters; add the cmin/cmax comparison in the
  self-inserted arm (§4.4). Update all callers (pass `InvalidCommandId`/nil
  for non-DML paths; pass the current command id for executor scans).

- **S8.3f** — Wire the command id from `Context.CmdID` into the executor's
  `TupleVisible` calls (the `scanMatching` sites in `operators_storage.go`
  and `operators_index.go`).

- **S8.3g** — Retire the fence maps: remove `CTEWriteFence`, `CTEXmaxReveal`,
  and all helpers from `operators_cte_dml.go`; remove the `cteFenced`/
  `cteRevealed` call sites from `operators_storage.go`.

- **S8.3h** — Verify: all CTE DML tests pass (especially
  `cte_dml_command_counter_test.go`, `cte_dml_preimage_reveal_test.go`,
  `cte_dml_outer_dml_fence_test.go`); `TestPort_IsolationEvalPlanQual` passes.

- **S8.3i** — Flip ledger row 2026-08-06 M0125-0055 (first row, "goopg
  still has no per-tuple command id") to `resolved`.

**Gates:** UNITS + the full CTE DML test family + isolation
(`TestPort_IsolationEvalPlanQual`) + SPOT + DS05.

### 5.3 Subtask ordering by risk

| order | subtask | risk | rationale |
|---|---|---|---|
| 1 | S8.2a-S8.2c | low | new type, no callers yet |
| 2 | S8.2d-S8.2f | medium | dispatch + plpgsql_runtime wiring; existing counter tests guard this |
| 3 | S8.3a-S8.3b | low | new functions, no callers; combo CID store is unused |
| 4 | S8.3c-S8.3d | medium | stamp path touches every DML write — but stamps are only CONSUMED after S8.3e |
| 5 | S8.3e-S8.3f | **high** | TupleVisible signature change touches every caller (~73 files per the explore agent) |
| 6 | S8.3g | low | removal only, after verification |
| 7 | S8.3h-S8.3i | — | verification + bookkeeping |

## 6. Risk assessment

| risk | severity | mitigation |
|---|---|---|
| TupleVisible signature change breaks a caller that wasn't updated | high | The Go compiler catches every missing argument; `InvalidCommandId`(0)/nil are safe defaults — no silent behavioural change |
| cmin=0 causes self-inserted tuples to be invisible to their own scan | medium | Analyzed in §4.4: a plain INSERT…RETURNING reads its own slot, not TupleVisible; scans that DO hit TupleVisible with curcid=0 correctly hide CTE-inserted tuples and show non-CTE tuples (whose cmin=0 ≥ curcid=0 is true → hidden — but for non-CTE inserts, the inserting plan never scans its own newly-inserted tuple through TupleVisible) |
| Performance regression from cmin/cmax stamping | low | One extra 4-byte write per tuple insert/update/delete and one comparison per TupleVisible call on self-inserted tuples; both are in cache lines already touched |
| Combo CID store grows unbounded | low | Bounded per transaction; cleared at COMMIT/ABORT. Worst case: N updates to the same row in one txn → N combo CIDs, each 8 bytes |
| WAL compatibility with PG standby | low | The field at offset 8 was always 0; becoming non-zero is valid PG t_cid — PG standby can consume it |

## 7. Open decisions

1. **Where does the `CommandCounter` live?** ✅ RESOLVED (S8.2):
       Stored per-`Context` (in `executor` package), not per-`mvcc.Manager`.
       The manager is shared across connections in goopg and cannot own
       per-connection counter state. The `executor.Context` persists across
       statements in a multi-statement simple-query batch and is already
       accessible at every site that needs the counter (dispatch,
       `plpgsql_runtime`, the fence-map helpers). A `Reset()` method and
       `ResetCommandCounter()` convenience method on Context support test
       paths that bypass the dispatch-driven lifecycle.

2. **Does `CommandCounterIncrement` also call `SnapshotSetCommandId`?**
   PG updates `ActiveSnapshot->curcid` on every increment so visibility
   checks use the latest command id. goopg doesn't have an active snapshot
   in the PG sense — `TupleVisible` takes the curcid as a parameter.
   Recommendation: no snapshot update needed for the initial implementation;
   the caller passes the curcid explicitly. If a future feature needs an
   implicit curcid, add it then.

3. **Should `FirstCommandId` be 0 or 1?** PG uses 0 (`InvalidCommandId` is
   `(CommandId) 0` for the error-not-a-command sentinel, but `FirstCommandId`
   is also `0` — the transaction starts at command 0). goopg's current `CmdID`
   is 0. Recommendation: keep `FirstCommandId = 0`, matching both PG and the
   existing goopg convention.

## 8. Design doc acceptance criteria

- [ ] Tuple header layout: `Xvac` → `t_cid`/`t_xvac` union with `HEAP_COMBOCID` flag
- [ ] `CommandCounter` type: transaction-owned, per-statement increment, lazy `used` guard
- [ ] `TupleVisible` signature: adds `curcid CommandId` and `combo *ComboCIDStore` parameters
- [ ] Fence maps retired: `CTEWriteFence`, `CTEXmaxReveal`, and all helpers removed
- [ ] All existing CTE DML tests pass with cmin/cmax providing the visibility
- [ ] `TestPort_IsolationEvalPlanQual` passes
- [ ] Catversion bumped
- [ ] Ledger row 2026-08-06 M0125-0055 (first row) flipped to `resolved`
