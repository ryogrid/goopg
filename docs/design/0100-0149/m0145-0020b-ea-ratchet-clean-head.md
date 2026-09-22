# M0145-0020b — EA ratchet at clean HEAD

Status: **COMPLETE 2026-09-22.**
Kind: recon
Parent: M0145-0020a

## Result

The estimate-accuracy ratchet fails at clean HEAD, before the Q39 grouped-output
cardinality repair. A detached worktree at `af524f5f2` captured all 99 TPC-DS
queries against the 20260907 baseline: 335 nodes were scored, 99 were unmatched
in PostgreSQL plans, and the scorer reported 56 findings with 17 NEW identifiers.
The capture and its complete command output are retained under the private
worktree in `tmp/m0145-0020a-ea-head/tmp/c20a/` and
`tmp/m0145-0020a-ea-head/tmp/m0145-0020b-clean-ea-final.log`.

## Comparison

The historical main-worktree run log records 61 NEW identifiers, so its exact
set differs from clean HEAD. That difference does not make the clean failure
new: both precede the uncommitted grouped-output repair. The current main-tree
JSON artifact has 53 identifiers; 14 overlap clean HEAD and only Q62, Q80, and
Q99 are clean-only. The ratchet cannot be used to reject M0145-0020a until its
baseline and capture environment are made reproducible.

## Clean-clone prerequisites

The first preserved attempt proved that the detached worktree lacks untracked
runtime inputs. Without the main checkout's PostgreSQL client directory,
`pg_isready` is absent and readiness fails despite the server listener binding.
Without the main checkout's generated TPC-DS query directory, the harness
produces an invalid zero-query pass. The valid audit supplied those read-only
paths through `PATH`, `EA_QDIR`, and `EA_PGDIR`; it did not write to a reference
cluster.

## Decision

Movement: none. The EA failure is pre-existing evidence, not a regression from
the Q39 repair. M0145-0020a remains uncommittable because its independent
TPC-H acceptance arm has the documented clean-HEAD Q9 600-second cancellation.
