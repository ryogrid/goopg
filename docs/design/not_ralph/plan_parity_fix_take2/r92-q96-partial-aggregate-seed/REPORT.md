# R92 REPORT — Q96 upper partial-aggregate source barrier

R92 is measurement-only. It made no persistent optimizer, executor, cost, or
plan behavior change. Scope commit `fd8f1210b` was reviewed before the
temporary diagnostic was introduced.

## Result

R91's Q96 trace establishes both sides of the apparent contradiction:

* the upper producer receives the rendered serial child and refuses its split
  at `agg-upper ... gate=subtree subtree=no-driving-scan`;
* the final search rel retains an accepted partial NLI path: `kind=5`, rows
  `24`, total `16367.34`, workers `3`, safe, and unparameterized.

The default-off R92 diagnostic copied paths before construction, declined any
tree containing `PathPrebuilt`, and recovered construction panics. It never
published or attached a throwaway node. Its Q96 record is:

`idx=0 kind=5 rows=24 total=16367.34 workers=3 safe=true aware=false reqouter=- bound=true build=prebuilt-source:depth=3,node=*optimizer.Filter unsafe=false gather=false noscan=true eligible=false`

Thus the path is not an executable upper worker source through the current
path-to-node API. Its depth-three `PathPrebuilt` wraps a shared `*Filter` node;
`createPlanNode` would stamp that prebuilt node and cannot be used as a
copy-safe reconstruction. The existing serial child also has no driving scan.
This is an exact representation barrier, not evidence that changing a cost,
enabling a Gather, or forcing Q96 would be sound.

## Controls and removal

The trace-enabled diagnostic Q96 EXPLAIN is byte-identical to R91's trace-on
control, SHA-256
`1abd71777dd802abf81a5704f85536a2be1a6a3b8579136568b7e9d58dfde828`.
Temporary binary and journals remain under `/tmp/pp2/r92/`. R91's
Q9/Q41/Q91/Q96 trace controls and Q96 PG value equality remain applicable
because R92 changed no final source behavior.

All temporary diagnostic code was removed. The final source has no R92
diagnostic helper or file; `git diff -- internal/optimizer` is empty. Final
local gates pass: `go test ./internal/optimizer`, `go vet ./internal/optimizer`,
and `git diff --check`.

## Next boundary

A future behavior round must separately represent a partial path's executable
node without sharing/stamping a `PathPrebuilt` node, while preserving NLI
parameter binding and one-driving-scan/double-Gather guards. It requires a new
reviewed scope and executor proof; R92 does not authorize construction.
