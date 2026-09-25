# R116 result: Q96 margin is not a legacy join-method or build-side decision

R116 observed the legacy/prebuilt explicit-join route identified by R115 on
the R111 common-input Goopg cluster. It made no production change. The
temporary source diagnostic was removed before this report.

## Controls

A private `/tmp/r116-goopg` binary ran the immutable R101 forms on the R111
data directory with 64 MB work memory, both collapse limits one,
`GOOPG_GATHER_PATHS=top`, `GOOPG_PARTIAL_AGG_PATHS=on`, `GOGC=off`, and
`GOMEMLIMIT=12GiB`. Trace-on plans are retained as
`/tmp/r116-{hdem-first,store-first}-on-{1,2}.{plan,json}` and records in
`/tmp/r116-goopg-server.log`. Trace-off repetitions are retained as
`/tmp/r116-{hdem-first,store-first}-off-{1,2}.plan`.

Each form was byte-identical across trace-off repetitions and both trace-on
text/JSON repetitions. Text SHA-256 values were hdem-first
`7e1d192167508a45f892ea3986b5a6345d752d8b54181e3748caee66f56bd7a9`; store-first
`72d60d7e04e0a3f9a430a6fc8eaaf7e3e694d24094554bd848d4dc3d6f385959`.
JSON values were hdem-first
`4062ac3fc2c7d4110d1cadd8a5811f7ad5afcc9dda79e6310d6261727e91ff8c`
and store-first
`1d45e82df60731e9c76c5f62ae2a1c7b620d9fcb6eed1441d57820d4dfe42d22`.
The retained `/tmp/r116-{hdem-first,store-first}.value` files both contain
`266`, matching R111's exact-result control.

## Attribution

The selected legacy join records were:

| form | join inputs | output rows | algorithm / build side | total |
| --- | --- | ---: | --- | ---: |
| hdem-first | 719876 × 7200 | 688465 | Hash / right | 14155.41 |
| hdem-first | 688465 × 12 | 658057 | Hash / right | 27620.75 |
| store-first | 719876 × 12 | 688081 | Hash / right | 14079.69 |
| store-first | 688081 × 7200 | 658057 | Hash / right | 27613.07 |

The selected constructor is `planner.go`'s explicit-join site; the
`pushdown.go` CROSS-promotion site emitted no record, and no selected join
has a small-dimension BuildLeft override or NLI rewrite. The final LATERAL
nested-loop join is outside the explicit-join constructor,
so its selected record deliberately has the diagnostic's explicit
`id=0` unmapped verdict. The two forced-form Hash Join records map one-to-one
to their constructor records. No CROSS-promotion mutation record occurred.

## Display-cost reconciliation

These totals are recursive EXPLAIN totals, not disjoint components and must
not be summed. The selected top Hash Join totals are 27620.75 and 27613.07,
whose difference is exactly 7.68. The LATERAL nested-loop totals are 27653.68
and 27646.00, again exactly 7.68; Aggregate, Sort, and Limit preserve that
difference through 27661.92 and 27654.24. Thus the first selected total that
differs at the root lineage is the top Hash Join. Its two child input
cardinalities differ because the preceding forced join is 688465 versus
688081, even though both produce the same final 658057 rows. This identifies
the observed first differing input as the intermediate cardinality; it does
not prove which legacy display-cost term turns that input into 7.68.

The completed temporary cost trace in `/tmp/r116-cost-server.log` supplies
the required self/child ledger. At the top Hash Join, hdem-first is
`childtotal=21040.18`, `selftotal=6580.57`, `total=27620.75`; store-first is
`childtotal=21032.50`, `selftotal=6580.57`, `total=27613.07`. Thus the 7.68
is entirely inherited child total, not a top-Hash self term. The unmapped
LATERAL node adds the same `selftotal=32.90` in both forms and preserves the
margin. This is display annotation observation only, never candidate pricing.

The Goopg root difference remains 7.68 in favour of store-first, but every
observed explicit join chose the same method and build side. Therefore neither
`chooseInnerJoinAlgo`, BuildLeft, nor the small-dimension override explains
the disagreement with PG's 175.91 hdem-first preference. The observed next
boundary is the cardinality/display-cost calculation within otherwise equal
legacy Hash Join shapes; it needs a separate scope.
