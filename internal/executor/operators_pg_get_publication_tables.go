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
}

func newPgGetPublicationTablesOp(p *planner.PgGetPublicationTables) *pgGetPublicationTablesOp {
	return &pgGetPublicationTablesOp{plan: p}
}

func (o *pgGetPublicationTablesOp) Schema() planner.Schema { return o.plan.Output() }

func (o *pgGetPublicationTablesOp) Open(ctx *Context) error {
	o.ctx = ctx
	o.rows = nil
	o.pos = 0
	// Resolve the publication-name filter set from the argument list.
	want, all, err := o.resolvePublicationFilter()
	if err != nil {
		return err
	}
	if o.ctx.PubSub == nil {
		return nil
	}
	pubs := o.ctx.PubSub.Publications()
	for _, pub := range pubs {
		if !all {
			if _, ok := want[pub.Name]; !ok {
				continue
			}
		}
		tables := o.publicationTables(pub)
		for _, t := range tables {
			oid := t.OID
			var relidDatum Datum
			if oid == 0 {
				relidDatum = NullDatum
			} else {
				relidDatum = NewIntDatum(int64(oid))
			}
			o.rows = append(o.rows, Row{
				relidDatum,
				NullDatum, // attrs (column list — goopg does not model column lists yet)
				NullDatum, // qual (row filter — same)
			})
		}
	}
	return nil
}

func (o *pgGetPublicationTablesOp) Close() error { return nil }

func (o *pgGetPublicationTablesOp) Next() (TupleSlot, error) {
	if o.pos >= len(o.rows) {
		return nil, EOF
	}
	row := o.rows[o.pos]
	o.pos++
	return SlotFromRow(nil, row), nil
}

// resolvePublicationFilter evaluates every argument and flattens the result
// into a set of publication names. NULL arguments are skipped. A bare empty
// argument list returns (nil, true) — meaning "no filter, accept all". The
// VARIADIC marker is implicit at this layer: an argument that evaluates to a
// text[] is spread element-by-element, and a scalar text argument is taken as
// a single name.
func (o *pgGetPublicationTablesOp) resolvePublicationFilter() (map[string]struct{}, bool, error) {
	if len(o.plan.Args) == 0 {
		return nil, true, nil
	}
	want := make(map[string]struct{})
	for _, a := range o.plan.Args {
		d, err := evalExpr(a, nil, o.ctx)
		if err != nil {
			return nil, false, err
		}
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
	return want, false, nil
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

// publicationTables resolves the set of tables a Publication advertises.
// AllTables expands to every base table in the catalog; otherwise the
// per-publication Tables list (qualified names) is resolved against the
// catalog so the returned *Table carries the live OID. Schema-only entries
// that no longer exist are silently skipped to mirror upstream's behaviour
// against a dropped table.
func (o *pgGetPublicationTablesOp) publicationTables(pub *catalog.Publication) []*catalog.Table {
	if o.ctx.Catalog == nil {
		return nil
	}
	if pub.AllTables {
		if in, ok := o.ctx.Catalog.(*catalog.InMemory); ok {
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
		t, ok := o.ctx.Catalog.LookupTable(parser.ObjectName{Schema: schema, Name: name})
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
