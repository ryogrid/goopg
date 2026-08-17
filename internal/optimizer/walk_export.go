package optimizer

// WalkPlanExprs invokes fn on every expression (recursively, via
// WalkExprTree) attached to every plan node walkPlanExprs models.
// Exported for the executor's SubPlan cacheability classifier
// (Stage 9 / D4.2): the walker is maintained here, next to the node
// definitions, so new node kinds extend exactly one enumeration.
// Note: fn does NOT descend into a sublink expression's inner Plan —
// callers that need that recurse explicitly on the sublink node.
func WalkPlanExprs(n Node, fn func(Expr)) { walkPlanExprs(n, fn) }

// WalkExprTree invokes fn on e and every sub-expression beneath it.
// Same sublink caveat as WalkPlanExprs.
func WalkExprTree(e Expr, fn func(Expr)) { walkExprTree(e, fn) }
