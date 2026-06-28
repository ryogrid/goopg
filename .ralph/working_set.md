(idle — nothing in flight)

Last landed (loop #6): M0118-0002 enabler (design 0118-0135) — point geometric
read support clearing predicate-gist's read-step blockers. NOT a promotion.

What landed:
- `point[0]`/`point[1]` 0-based coordinate subscript → float8 (PG geometric:
  [0]=X, [1]=Y). Executor returns the coordinate as numeric from the text-backed
  point; analyzer + planner type the element as float8 so `sum(p[0])` type-checks.
- `<<`/`>>` on points → strictly-left/right X-coordinate bool comparison
  (`p << q` ⇔ p.x < q.x). Integer bit-shift + name char-subscript untouched
  (parsePointText requires exactly two parseable floats → safe gate).
Files: internal/executor/expr.go (array_subscript + OpBitShift{Left,Right}),
internal/analyzer/analyzer.go, internal/planner/planner.go, two new test files.

KEY FINDING for next loop: predicate-gist's SOLE remaining divergence is goopg's
float8 TEXT OUTPUT. codec.go:458 encodes float8 with FormatFloat(f,'g',-1,64) →
scientific "2.23375e+06" where PG float8out prints plain "2233750". A filtered
probe proved ZERO SSI divergences (40001 behaviour already byte-identical). So:

  → NEXT (high-value, likely PROMOTION): a faithful float8out — shortest
    round-trip digits with PG's fixed-vs-scientific exponent threshold. MUST be
    its own loop: central display path (every float8 value), so validate against
    ./postgres/local_install across magnitudes AND re-run the FULL regress-port
    suite (codec/format change = high blast radius, project rule #5). Then
    predicate-gist promotes with no further SSI work.

Other remaining M0118 failed specs (both Effort-L unbuilt subsystems):
- predicate-gin: `int4[]` column type collapses to int4 + `array[1]` typed
  text[] (array-type system gap) + GIN AM.
- deadlock-parallel: parallel-worker lock groups (no parallel query in goopg).
