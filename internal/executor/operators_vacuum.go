package executor

import (
	"github.com/goopg/goopg/internal/catalog"
	"github.com/goopg/goopg/internal/parser"
	"github.com/goopg/goopg/internal/planner"
	"github.com/goopg/goopg/internal/storage"
	"github.com/goopg/goopg/internal/vacuum"
)

// vacuumOp executes a VACUUM statement, running heap page-prune on the
// target relations and updating the FSM with the resulting free space
// (M0046-0003). VACUUM without a target list vacuums all user tables.
type vacuumOp struct {
	plan *planner.Utility
	ctx  *Context
	done bool
}

func newVacuumOp(p *planner.Utility) *vacuumOp { return &vacuumOp{plan: p} }

func (o *vacuumOp) Schema() planner.Schema { return nil }

func (o *vacuumOp) Open(ctx *Context) error {
	o.ctx = ctx
	return nil
}

func (o *vacuumOp) Close() error { return nil }

// Next runs the VACUUM as a one-shot side effect. Errors are suppressed:
// a VACUUM failure should not abort the client session.
func (o *vacuumOp) Next() (Row, error) {
	if o.done {
		return nil, EOF
	}
	o.done = true
	if o.ctx == nil || o.ctx.Pool == nil || o.ctx.TxnMgr == nil {
		return nil, EOF
	}

	vs := o.plan.Stmt.(*parser.VacuumStmt)
	rels := o.vacuumTargets(vs)
	for _, rel := range rels {
		_, _ = vacuum.VacuumWithFSMAndVM(o.ctx.Pool, o.ctx.TxnMgr, rel, o.ctx.FSM, o.ctx.VM)
	}
	return nil, EOF
}

// vacuumTargets resolves the list of heap RelFileNodes to vacuum.
func (o *vacuumOp) vacuumTargets(vs *parser.VacuumStmt) []storage.RelFileNode {
	cat := o.ctx.Catalog
	if len(vs.Targets) > 0 {
		var out []storage.RelFileNode
		for _, name := range vs.Targets {
			tbl, ok := cat.LookupTable(name)
			if !ok || tbl.Virtual {
				continue
			}
			out = append(out, cat.RelFileNode(tbl))
		}
		return out
	}
	// No explicit targets: vacuum every non-virtual user table.
	if im, ok := cat.(*catalog.InMemory); ok {
		var out []storage.RelFileNode
		for _, tbl := range im.AllTables() {
			if !tbl.Virtual {
				out = append(out, cat.RelFileNode(tbl))
			}
		}
		return out
	}
	return nil
}
