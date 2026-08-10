(idle — nothing in flight)

Last loop: M0130-S11.4 slice **3b-3b landed** — and with it the whole S11.4
umbrella chain (3b, 3b-2, 3b-2c, 3b-3, S11.4) plus S11.5, all of which had been
unchecked only because 3b-3b was. Verdict: the filed blocker ("blocked until
every index is descriptor-bearing") asked the wrong question — both named
MAXALIGNs were already satisfied per-format, and the blob format will never have
them (permanent fallback, ledger row). The live gap was `_bt_form_posting`'s
TOTAL size MAXALIGN, which made goopg reject every posting a promoted PG writes.

Next loop: re-read the `## Current Priority` banner. M0130's remaining unchecked
work needs re-surveying — the S11.4/S11.5 subtrees are now fully checked, so the
next M0130 item may have to be newly filed (candidates from the ledger:
`XLOG_BTREE_DEDUP` producer, `_bt_swap_posting`, root-split image), or the banner
falls through to M-NIGHTLY / M0119.
