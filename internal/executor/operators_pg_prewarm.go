package executor

// pg_prewarm(regclass, mode text DEFAULT 'buffer', fork text DEFAULT 'main',
// first_block int8 DEFAULT NULL, last_block int8 DEFAULT NULL) → int8: the
// contrib/pg_prewarm SQL function (pg_prewarm.c). It loads a fork's blocks
// and returns how many it processed — 'buffer' reads them into shared
// buffers (ReadBufferExtended over a read stream), 'read' reads them from
// storage without keeping them, 'prefetch' asks for them asynchronously.
// 032_relfilenode_reuse uses it to fill a 1MB shared_buffers so the pages of
// a dropped database are evicted (M0149-0029).

import (
	"fmt"

	"github.com/goopg/goopg/internal/catalog"
	"github.com/goopg/goopg/internal/optimizer"
	"github.com/goopg/goopg/internal/storage"
)

func evalPgPrewarm(x *optimizer.FuncCall, slot SlotView, ctx *Context) (Datum, error) {
	invalid := func(msg, hint string) error {
		return &ExecError{Code: "22023", Pos: x.Pos(), Message: msg, Hint: hint}
	}
	arg := func(i int) (Datum, bool, error) {
		if i >= len(x.Args) {
			return NullDatum, false, nil
		}
		v, err := evalExprSlot(x.Args[i], slot, ctx)
		return v, true, err
	}
	relArg, _, err := arg(0)
	if err != nil {
		return NullDatum, err
	}
	if relArg.IsNull() {
		return NullDatum, invalid("relation cannot be null", "")
	}
	mode := "buffer"
	if v, given, err := arg(1); err != nil {
		return NullDatum, err
	} else if given {
		if v.IsNull() {
			return NullDatum, invalid("prewarm type cannot be null", "")
		}
		mode = v.StringValue()
	}
	switch mode {
	case "prefetch", "read", "buffer":
	default:
		return NullDatum, invalid("invalid prewarm type", `Valid prewarm types are "prefetch", "read", and "buffer".`)
	}
	forkName := "main"
	if v, given, err := arg(2); err != nil {
		return NullDatum, err
	} else if given {
		if v.IsNull() {
			return NullDatum, invalid("relation fork cannot be null", "")
		}
		forkName = v.StringValue()
	}
	fork, ok := parseForkName(forkName)
	if !ok {
		// forkname_to_number
		return NullDatum, invalid("invalid fork name", `Valid fork names are "main", "fsm", "vm", and "init".`)
	}

	oid, ok, err := resolveRegclassOID(x.Args[0], slot, ctx)
	if err != nil {
		return NullDatum, err
	}
	cat, isMem := ctx.Catalog.(*catalog.InMemory)
	if !ok || !isMem || ctx.Pool == nil {
		return NullDatum, &ExecError{Code: "42P01", Pos: x.Pos(),
			Message: fmt.Sprintf("could not open relation with OID %d", oid)}
	}
	rel, ok := relationStorageForOID(cat, oid)
	if !ok {
		if rel, ok = seedRelationStorage(ctx, oid); !ok {
			// RELKIND_HAS_STORAGE fails for views, composite types,
			// partitioned tables and foreign tables.
			return NullDatum, &ExecError{Code: "42809", Pos: x.Pos(),
				Message: fmt.Sprintf("relation \"%s\" does not have storage", prewarmRelName(cat, oid))}
		}
	}
	rel.Fork = fork
	// smgrexists before RelationGetNumberOfBlocksInFork: NBlocks would
	// create a missing fork.
	if !ctx.Pool.Exists(rel) {
		return NullDatum, invalid(fmt.Sprintf("fork \"%s\" does not exist for this relation", forkName), "")
	}
	nb, err := ctx.Pool.NBlocks(rel)
	if err != nil {
		return NullDatum, err
	}
	nblocks := int64(nb)
	first, last := int64(0), nblocks-1
	if v, given, err := arg(3); err != nil {
		return NullDatum, err
	} else if given && !v.IsNull() {
		first = v.Int
		if first < 0 || first >= nblocks {
			return NullDatum, invalid(fmt.Sprintf("starting block number must be between 0 and %d", nblocks-1), "")
		}
	}
	if v, given, err := arg(4); err != nil {
		return NullDatum, err
	} else if given && !v.IsNull() {
		last = v.Int
		if last < 0 || last >= nblocks {
			return NullDatum, invalid(fmt.Sprintf("ending block number must be between 0 and %d", nblocks-1), "")
		}
	}
	var done int64
	for blk := first; blk <= last; blk++ {
		if mode == "buffer" || mode == "read" {
			// 'read' (smgrread into a private buffer) goes through the pool
			// too: goopg has no storage read that bypasses it, and the
			// block's contents are the same.
			s, err := ctx.Pool.Pin(storage.BufferTag{Rel: rel, Block: storage.BlockNumber(blk)})
			if err != nil {
				return NullDatum, err
			}
			ctx.Pool.Unpin(s)
		}
		done++
	}
	return Datum{Kind: KindInt, Int: done}, nil
}

func prewarmRelName(cat *catalog.InMemory, oid uint32) string {
	if t, ok := cat.LookupTableByOID(oid); ok {
		return t.Name
	}
	return fmt.Sprint(oid)
}
