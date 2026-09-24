# M0145-0008c: PG-shared attribution and ea-ratchet repin

Status: complete 2026-09-24. The ea-ratchet baseline was repinned under the G4
PG-shared extension, and `make ea-ratchet` passes (52 vs 52).
Task: `.ralph/fix_plan.md` M0145-0008c (Kind: recon, Parent: M0145-0008).
Evidence: `analysis/m0145/m0145-0008c/`. It holds the per-key attribution, the
HEAD and pre-change findings and ratchet outputs, and the capture headers.

## What was NEW

At HEAD (`9520f622f`, code as of `bb90e51a4`) the ratchet reported three NEW
keys, all UNMATCHED-IN-PG (`pg_est` null):

| key | goopg est / actual | introduced by |
|---|---|---|
| `Q95:cte:ws_wh+customer_address+date_dim+web_sales+web_site` | 1 / 22 | M0145-0008 flip (the filed key) |
| `Q14:date_dim+store_sales` | 56 / 4927 | M0146-0002e (`bb90e51a4`) |
| `Q14:date_dim+item+store_sales` | 282 / 21264 | M0146-0002e (`bb90e51a4`) |

Attribution is an A/B on the same data clone and ANALYZE seed. At `a04b5c00e`
(before M0146-0002e) only the Q95 key is NEW. M0146-0002e did not run the
ratchet, so the two Q14 keys were found here. The ratchet is not in the M0146
gate list, and this is recorded in the ledger.

## Why each is PG-shared (G4, 2026-09-24 extension)

- **Q95:** the error is born in the 4-relation base join
  `{web_sales, web_site, date_dim, customer_address}` (goopg est 1, actual
  22). The semi join inherits it. PG's two nearest scopes are the same
  4-relation set and that set plus `ws_wh` and `web_returns`. PG estimates
  both at 1, off by the same 22x / 12x.
- **Q14, both keys:** the estimates did not change; the representative did.
  The scorer keeps a key's largest-actual occurrence. Before the change that
  was an accurately estimated 687k-row join. At HEAD that node is under a
  Gather and reports no actual, so the Q14b / Q14a filtered nodes became the
  representatives. PG never forms these relation sets (it joins
  `cross_items` first). Its nearest scope, `{cte:cross_items, date_dim, item,
  store_sales}`, is estimated at 1 in all 12 occurrences against 21135
  actual, a far worse underestimate than goopg's.

## Repin

`EA_CAPTURE=tmp/c20a/ea-capture.txt make ea-ratchet-repin` was run as a
standalone non-code commit. It adds the three keys and drops four FIXED ones:
Q49 ×2, `Q54:cte:segments`, and
`Q94:customer_address+date_dim+web_sales+web_site`. The last four were fixed
by the flip and by M0146-0002e.

## Found along the way

The scorer's largest-actual representative moves whenever a plan puts the
previous representative under a Gather (a Gather-internal node reports no
actual rows in parallel mode). A NEW key can therefore appear with no
estimator change at all. Q14 is the witness. Recorded as a ledger row
against the instrument.
