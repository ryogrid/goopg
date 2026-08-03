# M0125-0035 arm (a) — evidence

Loop #9, 2026-07-31. Design:
`docs/design/0125-0035a-preserved-side-restriction-descent.md`.

Host state: the nightly CI batch (`20260731-001201`) finished at 04:08 and the
host was verified quiet before every timed reading (load avg 0.14–0.9). Unlike
loop #8's, **the wall clocks in this directory are usable** — the SF0.5 sweep
did not need `FORCE=1`.

Binaries. `tmp/goopg-m0125-0035a-bin` is this change; `tmp/goopg-m0125-0035-bin`
is loop #8's, i.e. HEAD `89fb2384`, and is the A arm of the timed comparison.
The shared `tmp/goopg-bench-bin` was not written by any run here.

| file | what |
|---|---|
| `plan-diff-vs-0035.txt` | TPC-H `make plan-diff LABEL=m0125-0035-c2-qual-placement`, structural mode. 1/22 DIFFER (Q17), zero structural change. |
| `sweep-chunk1.txt` … `sweep-chunk4.txt` | full 99-query SF0.5 gate, `QUERIES=` chunks 1-25 / 26-50 / 51-75 / 76-99, all on the one binary. Totals `PASS=89 (54 ck-verified) MISMATCH=0 CKMISMATCH=0 ERROR=0 TIMEOUT=6 SKIP=4`. |
| `tpch-base/w2.tsv` | timed TPC-H w2 arm (warm ANALYZE + `GOOPG_RELSIZE_FALLBACK=2`, per-query-isolated harness) on loop #8's binary. |
| `tpch-new/w2.tsv` | the same arm on this change. |

Plan re-capture (all 18 timeout-class queries, both engines' arms live beside
each other): `analysis/m0125-0026-timeout-plans/goopg-warm-m0125-0035a/`.

## Reading the two TIMEOUT → PASS cells

Only two of the 95 non-SKIP cells changed against loop #8, and they must not be
read the same way.

- **Q31 → PASS 11 s, 19 rows, `ck=2a74acfb556c21a7`.** Attributable. Q31 is one
  of the two plans this change touches, and the touch is exactly the gap
  `M0125-0035`'s task body described: its six `CTE Scan` nodes now each carry
  `(d_qoy = N) AND (d_year = 1999)` instead of one carrying it and five being
  hoisted into a conjunction on the top join.
- **Q18 → PASS 266 s.** *Not* attributable, and not claimed. Q18's plan is
  byte-identical across the change; loop #8's TIMEOUT was measured under the
  live nightly. A 266 s reading against a 300 s cap on a quiet host is the
  economical explanation. It stays filed as `M0125-0033`.

Everything else is a non-event by design: all 87 common PASSes agree with loop
#8 on status **and** on value checksum.

## Reading the timed arm

Stream over the 21 completing queries 395.5 s → 389.1 s (−1.6 %); Q17, the only
query whose plan changed, 29.48 → 24.71 s (0.84×). **Both are inside the
±17 % single-run per-query noise band `M0125-0031` measured on this harness**,
so the correct verdict is *neutral* — the change is not paid for in TPC-H time,
which is what the bar asks, and it is not evidence of a speedup. Row counts are
identical on all 21. Q21 times out in both arms, as in all four arms of
`M0125-0031` (`M0125-0032`).
