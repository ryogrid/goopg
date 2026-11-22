# Function scan costs (M0149-0001h1a80)

## Problem

goopg priced a FROM-clause function scan as cpu\_tuple\_cost per row and nothing else:

- `generate_series(now(), now() + '1 day', '1 hour')` printed `0.00..0.25`; PG prints `0.01..0.26`.
- Inside a join, the scan was a sub\-plan leaf, which charged cpu\_tuple\_cost per row a second time. A Hash Join of
  two series printed `0.16..0.45`; PG prints `0.12..0.30`.
- `unnest(array[1,2,3])` estimated 1 row, and a SETOF SQL function also 1 row. PG estimates 3 and 1000.

## PG behaviour

cost\_functionscan \(costsize.c\):

- A function scan runs its function to completion before returning a row, so cost\_qual\_eval of the call —
  procost plus the arguments — is startup cost.
- Each returned tuple then costs cpu\_tuple\_cost plus the restriction quals.
- set\_function\_size\_estimates takes the support function's row count where one exists: generate\_series's,
  array\_unnest\_support's array length. Otherwise it takes prorows: ROWS, else 1000.

## Design

- internal/optimizer/functionscan\_cost.go:
  - `functionScanCallOps` is cost\_qual\_eval of the call for GenerateSeries, FromUnnest \(including
    string\_to\_table and the multi\-array form\) and UserSrfScan, with the routine's procost.
  - `functionScanStartup` and `costFunctionScanLeaf` build cost\_functionscan from it.
- DeriveLegacyDisplayCost prices these scans as startup = call cost and total = startup + cpu\_tuple\_cost × rows.
  A Filter directly over one is the scan's baserestrictinfo: cpu\_tuple\_cost plus the qual's cost per returned
  tuple, which replaces cpu\_tuple\_cost per surviving row.
- The join search prices a function\-scan leaf with `costFunctionScanLeaf` before the sub\-plan floor.
- EstimateRows: FromUnnest returns estimate\_array\_length \(the longest array in the multi\-array form\);
  UserSrfScan over a SETOF routine returns its prorows.

## Not done

- `isFunctionScanNode` in materialize.go still leaves FromUnnest and UserSrfScan out of the T\_FunctionScan class
  for rescan and materialisation decisions.
- Found in the probes but not specific to function scans:
  - a lateral Seq Scan filter is priced 10.02 where PG has 17.50;
  - `a = b + 1` over two series estimates 1 row where PG estimates 10;
  - a scan under `count(*)` prints width 4 where PG prints 0.
