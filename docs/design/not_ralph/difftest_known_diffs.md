# Differential Known Diffs

Inputs where the two parsers disagree, with disposition. Policy
(04-testing-and-gates.md §2): disposition (a) = fix new parser toward
upstream AND update the legacy test (documented delta); (b) = keep legacy
behavior via goopg_ext.y rule + note.

| input class | legacy behavior | yacc behavior | upstream truth | disposition |
|---|---|---|---|---|
| `-5` / `-3.14` (unary minus on numeric literal) | `UnaryOp{OpUnaryNeg}` over positive const | folded negative const (`gram.y doNegate`, :10874) | folded const (ruleutils prints negated consts with explicit cast) | (b)-inverted: yacc is RIGHT; legacy divergence ledgered for a future legacy-side fix or made moot at cutover. Pinned by TestKnownDiffUnaryMinusFold. |
| `SELECT ALL a` | syntax error ("expected expression (got all)") | accepted (`opt_all_clause`) | accepted | same shape as above; pinned by TestKnownDiffSelectAll. |

| UNION/INTERSECT chain tree shape | upstream gram.y builds LEFT-nested trees (%left); goopg legacy+new parser build RIGHT-nested chains on the single SetOp slot | same as legacy (intentional parity) | semantically equivalent for set ops; revisit only if a planner consumer depends on nesting side |

Add rows only with a test pinning BOTH sides.

Shared gaps (both parsers reject an accepted-upstream construct) are NOT
known-diffs rows — they belong to TODO sub-items (e.g. GROUP BY
DISTINCT/ALL set_quantifier lives under P1.3a with the grouping-set
constructs).
