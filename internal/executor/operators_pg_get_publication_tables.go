package executor

// operators_pg_get_publication_tables.go — pg_get_publication_tables(VARIADIC text[]) SRF.
//
// Returns one row per (publication, table) pair in the supplied publication
// name set. The argument list accepts either a single text[] (the VARIADIC
// spread shape libpqrcv emits) or any number of plain text values; an empty
// argument list returns every (publication, table) pair the PubSub registry
// holds. The output schema is (relid oid, attrs text, qual text); attrs and
// qual are NULL since goopg does not yet model column lists or row filters
// on publications. M0103-0008 probe-survival.

import (
	"sort"
	"strings"

	"github.com/goopg/goopg/internal/catalog"
	"github.com/goopg/goopg/internal/parser"
	"github.com/goopg/goopg/internal/planner"
)

type pgGetPublicationTablesOp struct {
	plan *planner.PgGetPublicationTables
	ctx  *Context
	rows []Row
	pos  int

	// outerSlot carries the lateral outer row when the op sits on
	// the right of a `Join.Lateral == true` (M0103-0008 rung 6).
	// Bound by the parent joinOp via BindLateralOuter before each
	// per-outer-row Open. nil means "no lateral binding" — Args
	// must reduce to constants in that case (the original
	// non-lateral SRF entry path).
	outerSlot SlotView
}

func newPgGetPublicationTablesOp(p *planner.PgGetPublicationTables) *pgGetPublicationTablesOp {
	return &pgGetPublicationTablesOp{plan: p}
}

func (o *pgGetPublicationTablesOp) Schema() planner.Schema { return o.plan.Output() }

func (o *pgGetPublicationTablesOp) Open(ctx *Context) error {
	o.ctx = ctx
	o.rows = nil
	o.pos = 0
	// Evaluate every argument expression to a Datum, then delegate to the
	// shared row-builder. ProjectSet (M0103-0008 final sub-step) reuses the
	// same builder once the publisher's libpqrcv probe has materialised the
	// aggregated text[] of publication names. The outerSlot is non-nil only
	// when a parent Join.Lateral has bound an outer row (M0103-0008 rung 6);
	// passing it through evalExprSlot lets ColumnRefs into the outer FROM
	// sibling resolve.
	args := make([]Datum, 0, len(o.plan.Args))
	for _, a := range o.plan.Args {
		d, err := evalExprSlot(a, o.outerSlot, ctx)
		if err != nil {
			return err
		}
		args = append(args, d)
	}
	rows, err := buildPgGetPublicationTablesRows(ctx, args)
	if err != nil {
		return err
	}
	o.rows = rows
	return nil
}

func (o *pgGetPublicationTablesOp) Close() error { return nil }

// BindLateralOuter binds the outer row's slot for lateral arg
// evaluation. Called by joinOp before each per-outer-row Open when
// `plan.Lateral == true`. Passing nil clears the binding.
func (o *pgGetPublicationTablesOp) BindLateralOuter(slot SlotView) {
	o.outerSlot = slot
}

func (o *pgGetPublicationTablesOp) Next() (TupleSlot, error) {
	if o.pos >= len(o.rows) {
		return nil, EOF
	}
	row := o.rows[o.pos]
	o.pos++
	return SlotFromRow(nil, row), nil
}

// buildPgGetPublicationTablesRows is the shared row-builder reused by both
// the FROM-clause SRF operator and ProjectSet's per-input-row evaluator
// (M0103-0008). args carries the already-evaluated argument Datums; an
// empty list returns every (publication, table) pair the registry holds.
func buildPgGetPublicationTablesRows(ctx *Context, args []Datum) ([]Row, error) {
	want, all := resolvePublicationFilterFromDatums(args)
	if ctx.PubSub == nil {
		return nil, nil
	}
	pubs := ctx.PubSub.Publications()
	rows := make([]Row, 0)
	for _, pub := range pubs {
		if !all {
			if _, ok := want[pub.Name]; !ok {
				continue
			}
		}
		tables := publicationTablesForCtx(ctx, pub)
		for _, t := range tables {
			oid := t.OID
			var relidDatum Datum
			if oid == 0 {
				relidDatum = NullDatum
			} else {
				relidDatum = NewIntDatum(int64(oid))
			}
			rows = append(rows, Row{
				relidDatum,
				NullDatum, // attrs (column list — not modeled)
				NullDatum, // qual (row filter — not modeled)
			})
		}
	}
	return rows, nil
}

// resolvePublicationFilterFromDatums flattens every argument Datum into a
// publication-name set. NULL Datums are skipped. An empty argument list
// signals "no filter, accept all". Datums whose StringValue is a
// brace-wrapped text-array are spread element-by-element.
func resolvePublicationFilterFromDatums(args []Datum) (map[string]struct{}, bool) {
	if len(args) == 0 {
		return nil, true
	}
	want := make(map[string]struct{})
	for _, d := range args {
		if d.IsNull() {
			continue
		}
		for _, name := range flattenTextArg(d) {
			name = strings.TrimSpace(name)
			if name != "" {
				want[name] = struct{}{}
			}
		}
	}
	return want, false
}

// flattenTextArg decodes a Datum produced by a publication-name argument into
// a list of strings. text[] payloads come through as a brace-wrapped string
// (goopg's StringValue format for array Datums) — split on commas with
// rudimentary escape handling. Plain text values pass through as-is.
func flattenTextArg(d Datum) []string {
	s := d.StringValue()
	if s == "" {
		return nil
	}
	if len(s) >= 2 && s[0] == '{' && s[len(s)-1] == '}' {
		return parseTextArray(s)
	}
	return []string{s}
}

// publicationTablesForCtx resolves the set of tables a Publication advertises.
// AllTables expands to every base table in the catalog; otherwise the
// per-publication Tables list (qualified names) is resolved against the
// catalog so the returned *Table carries the live OID. Schema-only entries
// that no longer exist are silently skipped to mirror upstream's behaviour
// against a dropped table.
func publicationTablesForCtx(ctx *Context, pub *catalog.Publication) []*catalog.Table {
	if ctx.Catalog == nil {
		return nil
	}
	if pub.AllTables {
		if in, ok := ctx.Catalog.(*catalog.InMemory); ok {
			out := in.AllTables()
			sort.Slice(out, func(i, j int) bool {
				if out[i].Schema != out[j].Schema {
					return out[i].Schema < out[j].Schema
				}
				return out[i].Name < out[j].Name
			})
			return out
		}
		return nil
	}
	out := make([]*catalog.Table, 0, len(pub.Tables))
	for _, qname := range pub.Tables {
		schema, name := splitQualifiedTable(qname)
		t, ok := ctx.Catalog.LookupTable(parser.ObjectName{Schema: schema, Name: name})
		if !ok {
			continue
		}
		out = append(out, t)
	}
	return out
}

// splitQualifiedTable splits "schema.name" into ("schema", "name");
// returns ("", qname) if there is no dot.
func splitQualifiedTable(qname string) (string, string) {
	i := strings.IndexByte(qname, '.')
	if i < 0 {
		return "", qname
	}
	return qname[:i], qname[i+1:]
}
