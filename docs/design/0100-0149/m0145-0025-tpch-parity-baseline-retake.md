# M0145-0025 — TPC-H plan-parity baseline re-taken on the pinned-seed lane

Status: complete 2026-09-23. Task: `.ralph/fix_plan.md` M0145-0025 (Kind:
recon, Parent: M0145-0021b). A capture and blessing only; no production code.

## Why

M0145-0021b found that `scripts/tpch-estimate-audit-arm.sh` never pinned
`GOOPG_ANALYZE_SEED`, so the canonical TPC-H parity lane (M0144-0001) carried
A/A noise (`same=1/21 changed=20`). Every TPC-H `CATEGORIES-EXCL-MATCH`
figure captured before the pin may be noise, and no capture may be diffed
across the pin. This task re-takes the lane's base.

## Capture

`PGSHAPED=1 JOINTREE=0 scripts/jointree-parity-capture.sh tpch <label>
tmp/m0145-0025`, twice (`ship-a`, `ship-b`), at `318dd9f3d`. The engine is
unchanged since `564dc1c8e`; the served binary's sha256 is `5f160680d3a8…`,
verified by the script. Each run uses a private clone of `:65433` (online
`pg_basebackup`, shared server untouched), the parallel canonical mode
(`-serial=false`), and seed `20260905`. The PG arm is a bare
`estimate-audit` against `:65432` (read-only, `-warm-stats=false`).

- **`PGSHAPED=1` is deliberate.** The capture script's arm defaults to
  `PGSHAPED=0`, the legacy DP search, which is not what ships (owner call
  2026-09-22 after M0145-0020a, recorded in `scripts/tpch-acceptance-arm.sh`'s
  `PGSHAPED` note: Q9 >600 s on the legacy search vs 2.8 s shipped). A
  `PGSHAPED=0` control capture on the same binary reads a different
  result (below), so the flag must be named in every future comparison.

## Result

| capture | goopg plans sha256 | PLAN-PARITY | CATEGORIES-EXCL-MATCH |
|---|---|---|---|
| ship-a (`PGSHAPED=1`) | `952c4a77529bc803…` | match=2/22 (Q6, Q11) | join-order=15 join-method=10 scan-type=10 parameterisation=6 aggregation-strategy=7 sort-strategy=11 parallelism=15 qual-placement=4 rendering=2 |
| ship-b (`PGSHAPED=1`) | `952c4a77529bc803…` (byte-identical) | same | same |
| control (`PGSHAPED=0`) | `0048d9f62e0a8ccd…` | match=1/22 | join-order=18 join-method=11 scan-type=13 parameterisation=6 aggregation-strategy=5 sort-strategy=9 parallelism=19 qual-placement=5 rendering=2 |

PG arm sha256 `548ceee53b0f4f11…` on every run. Divergence classes
(ship): `D1-sublink=2 D3-partialpath=10 D4-upperrel=5 jointree-search=3`.

**A/A is exact**: two shipped-configuration captures are byte-identical, so
the seed pin removed the noise M0145-0021b measured.

## Blessed base

The ship-a capture is blessed as the TPC-H parity lane's comparison base,
git-tracked under `analysis/m0145/`:
- `m0145-0025-tpch-goopg-parallel.plans.txt` (+ `.txt` sidecar);
- `m0145-0025-tpch-pg-parallel.plans.txt` (+ `.txt` sidecar);
- `m0145-0025-tpch-diff.txt` and `m0145-0025-tpch-class.txt`.

Future TPC-H parity deltas diff against these, with `PGSHAPED=1` and the
seed pin.

## Superseded figures

- **M0144-0001's floor re-pin (match=1/22, Q6)**
  (`analysis/m0144/m0144-0001-*-parallel.plans.txt`) predates the seed pin
  and so is superseded. Its category line (join-order=18 join-method=11
  scan-type=13 … parallelism=20) sits close to this task's `PGSHAPED=0`
  control and far from the shipped figures, which suggests it measured
  the legacy DP search as well. That is an inference, not verified: the
  artefact does not record the flag.
- The M0145-0002 TPC-H on/off captures (`analysis/m0145/m0145-0002-tpch-*`)
  and every TPC-H `CATEGORIES-EXCL-MATCH` line in commit bodies before
  M0145-0021b's pin are not comparable with this base.
- The "TPC-H 2/22 on BOTH arms at `PGSHAPED=1`" reading in M0145-0008's
  record is consistent with this base (match=2/22). It was measured before
  the pin, so it is confirmed rather than superseded.
- The `AGENT.md` §Goal TPC-H floor is the owner's to re-pin. This base
  reads match=2/22 against the provisional floor of 3.
