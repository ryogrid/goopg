# 08-11 — Protocol/syscall corking (batch the per-query reply frames)

status: design · date: 2026-07-14 · base: `a640d2b0` · gates: G-unit, G-perf,
G-waldump (protocol wire parity) → [README](README.md)

## 1. Problem and numbers

The read path spends ~20 % of `-S` CPU in socket syscalls and ~18–19 % in
protocol assembly + flush (06-03: 19.2 %; confirmed at scale 500, 07-02: socket
syscalls 19.8 %, `FrameWriter.Flush`/`WriteReadyForQuery` ~17.8 %). Each
point-`SELECT` reply is DataRow + CommandComplete + ReadyForQuery, and the flush
shape drives one-or-more `write()` syscalls per query. PostgreSQL assembles the
same frames but more cheaply and corks them into fewer syscalls.

## 2. Current-code map (verified at `a640d2b0`)

- **`FrameWriter.WriteDataRow`** — `internal/protocol/messages.go:306` (and the
  reuse variant `WriteDataRowReuse` :337).
- **`FrameWriter.WriteCommandComplete`** — `messages.go:356`.
- **`FrameWriter.WriteReadyForQuery`** — `messages.go:138`.
- **`FrameWriter.Flush`** — `internal/protocol/frame.go:247`: the flush to the
  underlying `bufio.Writer` → `net.Write`. The 07 profile shows
  `WriteReadyForQuery` → `Flush` → `bufio.Flush` → `net.Write` as ~17.8 % cum.

## 3. PostgreSQL reference

- `src/backend/libpq/pqcomm.c` — `pq_putmessage` buffers into `PqSendBuffer`;
  `pq_flush` writes once when the buffer fills or the command ends
  (`ReadyForQuery`). PG accumulates DataRows and flushes at command boundary, so
  a single-row `SELECT` is one `send()` for DataRow+CommandComplete+
  ReadyForQuery, not three.

## 4. Target design

Ensure DataRow + CommandComplete + ReadyForQuery for one query coalesce into a
**single** buffered flush / `writev`:

- Do not flush between the three frames of one reply; flush once at
  ReadyForQuery (the command boundary).
- Where multiple frames are already buffered, prefer a single `writev` of the
  buffered segments over per-frame `net.Write` (reduces syscall count).
- Slim per-frame bookkeeping (length prefixing, header allocation) on the
  hot DataRow path — `WriteDataRowReuse` (`messages.go:337`) already targets
  this; extend the reuse to the full reply.

### Decision log

- **D1 — wire format is byte-identical.** This is purely *when* bytes are
  flushed, not *what* bytes — the protocol stream must be unchanged
  (a real PG client / `pg_waldump`-style parity must not notice). G-waldump is
  not directly relevant, but a wire-parity test is.
- **D2 — cork at the command boundary, matching PG.** Flushing at
  ReadyForQuery is exactly PG's `pq_flush` timing; no new heuristic.

## 5. Invariants and failure modes

- **I1 — no reply withheld past its command.** ReadyForQuery must still flush
  before the server reads the next client message, or the client hangs. The cork
  releases at the command boundary, not later.
- **F1 — large result sets.** A multi-row `SELECT` must still flush when the
  send buffer fills (PG's `pq_flush` on full buffer), not accumulate unbounded.
- **F2 — extended protocol.** Bind/Execute/Sync framing has its own flush points
  (Sync is the boundary); the corking must respect them (coordinate with doc 09).

## 6. Migration slices

| # | slice | content | gates |
|---|---|---|---|
| S1 | cork at command boundary | remove intra-reply flushes; flush once at ReadyForQuery; buffer-full fallback. | G-unit (wire parity) |
| S2 | writev the buffered segments | single `writev` for multi-frame replies where beneficial. | G-unit, G-perf |
| S3 | perf acceptance | `-S`: syscall + flush CPU share drops; TPS rises. | G-perf |

## 7. Test-impact matrix

| test | file | slice |
|---|---|---|
| protocol frame encoding/parity | `internal/protocol/*_test.go` | S1, S2 |
| extended-protocol framing | `internal/server/extended_test.go` | S1 (respect Sync boundary) |
| real-client smoke (pgbench) | commit hook | S3 |

## 8. Performance verification

`-S` at scale 100: socket-syscall (~20 %) + protocol-flush (~18 %) CPU shares
drop; syscall count per query (measurable via `strace -c` on a short run)
approaches one `send` per reply. TPS rises toward the ceiling.

## 9. Open questions

- **O-PC-1** — Does goopg already cork some of this (`WriteDataRowReuse`
  exists)? Profile a single-query `strace` to count the actual syscalls per
  reply before deciding S1 vs S2 priority.
- **O-PC-2** — `writev` vs `bufio` copy: at these row sizes, is `writev` actually
  fewer cycles than copying into one buffer and one `write`? Micro-benchmark.
