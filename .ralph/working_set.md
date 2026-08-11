(idle — nothing in flight)

M0131-S30.1b FIXED and committed (loop #149). Root cause was in the WRITER, not
the reader: `emitSegmentPad` wrote the cross-segment `XLOG_NOOP` pad as
`boundary-gapStart` RAW record bytes, so a gap spanning a page boundary
overwrote the page-header slot with the pad's own `xl_info`/`xl_rmid`
(`0x20`/`0` — the measured `magic=0x0020`) and overran the boundary. Pad is now
sized to the gap minus `pageHeaderBytesIn(...)` and emitted through
`emitWithPageHeaders`. Design `docs/design/0131-0028`.

Gates: units precommit PASS; `internal/wal` PASS; `RUNS=3 crashprobe30`
**OVERALL: PASS (3 runs)** — first full-green S30 gate, no early end-of-WAL in
any restart log. Negative control reproduces the production signature byte for
byte.

Next loop: banner still puts M0131 top. Remaining S30 items are **S30.2**
(an early end-of-WAL must refuse to start, not WARN-and-open) and **S30.4**
(no checkpoint fires during ~180 MiB of WAL), plus **S30.6** (the two WAL
append paths disagree about segment-boundary layout). S30.2 is the one that
makes every future truncation loud instead of silent.
