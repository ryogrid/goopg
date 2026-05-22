// plannode.go — Phase C.3 PlanNode sum-type (M0107-0003).
//
// PlanNode is a tagged-union that compactly represents the planning
// metadata for a statement's operator tree, mirroring the OpNode
// sum-type for execution and ExprNode for expression evaluation.
//
// The plan slab (planTreeSlab) is built alongside the opTreeSlab and
// exprTreeSlab during BuildFast. For concrete plan-node kinds
// (PlanFilter, PlanLimit), metadata is stored inline in payload bytes
// so that the execution-time state structs (filterState, limitState)
// hold only primitive values — no GC-traced planner.Node references.
//
// PlanAdapter preserves full planner.Node semantics for plan nodes that
// have not yet been migrated (SeqScan, Project, and all others).
//
// Migration status (Phase C.3):
//   fully concrete (no planner.Node reference after buildRec):
//     PlanFilter  — predIdx int32 (indexes exprTreeSlab)
//     PlanLimit   — limitExprIdx, offsetExprIdx int32
//     PlanSeqScan — seqScanOp holds schema/tbl/pos/rel directly; no orig pointer
//   adapter (keeps orig planner.Node for future migration):
//     PlanProject, PlanAdapter (generic)

package executor

import (
	"encoding/binary"

	"github.com/goopg/goopg/internal/planner"
)

// PlanKind discriminates PlanNode variants in the plan-tree slab.
type PlanKind uint8

const (
	PlanInvalid PlanKind = iota
	PlanFilter           // payload[0:4] = predIdx int32
	PlanLimit            // payload[0:4] = limitExprIdx, payload[4:8] = offsetExprIdx
	PlanSeqScan          // seqScanOp is concrete; PlanNode not used in buildRec for SeqScan
	PlanProject          // orig = *planner.Project (future: raw bytes)
	PlanAdapter          // orig = generic planner.Node
)

// noPlan is the sentinel "no plan child" index, analogous to noChild for OpNode.
const noPlan = int32(-1)

// planPayloadSize is the number of inline bytes per PlanNode for per-kind data.
// Sized to hold two int32 values (PlanLimit's limitExprIdx and offsetExprIdx).
const planPayloadSize = 16

// PlanNode is a tagged-union plan-tree node stored in a planTreeSlab.
//
// For PlanFilter and PlanLimit, all needed metadata is in payload bytes
// and orig is nil — no GC-traced planner.Node reference during execution.
//
// For PlanAdapter (and unmigrated PlanSeqScan, PlanProject), orig holds
// the original planner.Node so the GC keeps it alive during the statement.
type PlanNode struct {
	Kind    PlanKind
	_pad    [3]byte
	childA  int32              // primary plan child; noPlan if none
	childB  int32              // secondary plan child; noPlan if none
	payload [planPayloadSize]byte
	orig    planner.Node // non-nil only for Adapter and unmigrated nodes
}

// planTreeSlab is the flat slice of PlanNode for a single statement.
// It is appended to during BuildFast and immutable thereafter.
type planTreeSlab []PlanNode

// buildPlanFilter appends a PlanFilter node and returns its index.
// predIdx is the exprTreeSlab index for the compiled predicate.
func (s *planTreeSlab) buildPlanFilter(predIdx int32, childA int32) int32 {
	idx := int32(len(*s))
	n := PlanNode{Kind: PlanFilter, childA: childA, childB: noPlan}
	binary.LittleEndian.PutUint32(n.payload[0:], uint32(predIdx))
	*s = append(*s, n)
	return idx
}

// PlanFilterPredIdx returns the exprTreeSlab index for PlanFilter's predicate.
func PlanFilterPredIdx(n *PlanNode) int32 {
	return int32(binary.LittleEndian.Uint32(n.payload[0:]))
}

// buildPlanLimit appends a PlanLimit node and returns its index.
// limitExprIdx and offsetExprIdx are exprTreeSlab indices (noExpr if absent).
func (s *planTreeSlab) buildPlanLimit(limitExprIdx, offsetExprIdx int32, childA int32) int32 {
	idx := int32(len(*s))
	n := PlanNode{Kind: PlanLimit, childA: childA, childB: noPlan}
	binary.LittleEndian.PutUint32(n.payload[0:], uint32(limitExprIdx))
	binary.LittleEndian.PutUint32(n.payload[4:], uint32(offsetExprIdx))
	*s = append(*s, n)
	return idx
}

// PlanLimitExprs returns the exprTreeSlab indices for LIMIT and OFFSET expressions.
// Either may be noExpr (-1) if the corresponding clause is absent.
func PlanLimitExprs(n *PlanNode) (limitIdx, offsetIdx int32) {
	return int32(binary.LittleEndian.Uint32(n.payload[0:])),
		int32(binary.LittleEndian.Uint32(n.payload[4:]))
}

// buildPlanAdapter appends a PlanAdapter node that keeps orig alive.
func (s *planTreeSlab) buildPlanAdapter(orig planner.Node, childA int32) int32 {
	idx := int32(len(*s))
	*s = append(*s, PlanNode{Kind: PlanAdapter, childA: childA, childB: noPlan, orig: orig})
	return idx
}
