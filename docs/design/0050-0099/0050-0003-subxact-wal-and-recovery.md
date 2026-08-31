# Subxact WAL and Recovery — M0050-0003

| field      | value                         |
|------------|-------------------------------|
| status     | accepted                      |
| date       | 2026-05-05                    |
| supersedes | —                             |

## 1. Problem

Crash recovery must rebuild the subxact-to-parent map and mark
individually-aborted subxacts correctly. Without WAL records for these
operations, replay would see subxact-XID-tagged rows but not know whether
they were rolled back (ROLLBACK TO SAVEPOINT) or committed with the parent.

## 2. Design

### 2.1 New record kinds (`internal/wal/recovery.go`)

| Constant | Value | Payload |
|---|---|---|
| `RecordKindXactAssignment` | 15 | `kind(1) \| parentXid(4) \| count(2) \| subXids[count](4 each)` |
| `RecordKindXactRollbackTo` | 16 | `kind(1) \| parentXid(4) \| count(2) \| abortedSubXids[count](4 each)` |
| `RecordKindXactSubAbort` | 17 | `kind(1) \| subXid(4)` = 5 bytes |

### 2.2 Encode/decode functions

```go
func EncodeXactAssignment(parentXid, subXids) []byte
func DecodeXactAssignment(payload) (parentXid, subXids, err)

func EncodeXactRollbackTo(parentXid, abortedSubXids) []byte
func DecodeXactRollbackTo(payload) (parentXid, abortedSubXids, err)

func EncodeXactSubAbort(subXid) []byte
func DecodeXactSubAbort(payload) (subXid, err)
```

All use LittleEndian encoding consistent with existing WAL records.

### 2.3 `ApplyRecord` no-op treatment

Physical crash recovery (`ApplyRecord`) treats all three new kinds as
no-ops — like `XactCommit`/`XactAbort`. The subxact-to-parent map is
populated by the MVCC manager recovery path (`Manager.RegisterSubXid` /
`Manager.MarkSubxactAborted`), which is wired in M0050-0004 via the
initdb recovery driver.

### 2.4 Emission points (M0050-0004 wires these)

| Event | Record emitted |
|---|---|
| Subxact first write (lazy XID allocation) | `XactAssignment` |
| `ROLLBACK TO SAVEPOINT` | `XactRollbackTo` |
| Subxact abort without savepoint | `XactSubAbort` |

### 2.5 WAL growth

Each assignment record is 7 + 4×N bytes where N = number of subxacts
lazily allocated in that batch. For 100 savepoints, worst-case total is
7 + 400 = 407 bytes per assignment + one XactRollbackTo per ROLLBACK TO.
This is negligible compared to data records.

## 3. Tests (`internal/wal/subxact_wal_test.go`)

| Test | Coverage |
|---|---|
| `TestEncodeDecodeXactAssignment` | Round-trip encode/decode with multiple subXids |
| `TestEncodeDecodeXactRollbackTo` | Round-trip encode/decode with aborted subXids |
| `TestEncodeDecodeXactSubAbort` | Round-trip single subXid |
| `TestXactAssignmentEmptySubXids` | Empty subXids list round-trips correctly |
| `TestSubxactWALReplayRoundTrip` | **DoD**: all 3 records written to WAL and read back correctly via `ReadAll` |
| `TestSubxactApplyRecordSkipsNoOp` | `ApplyRecord` returns `(false, nil)` for all 3 kinds |
