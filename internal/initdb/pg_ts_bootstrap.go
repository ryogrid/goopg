package initdb

import (
	"fmt"
	"sort"

	"github.com/goopg/goopg/internal/catalog"
	"github.com/goopg/goopg/internal/executor"
)

// Bootstrap text-search catalogs (M0149-0001g slice 20): the rows
// postgres.bki seeds — the default parser, four templates, the "simple"
// dictionary and configuration and its token map — generated into
// pg_ts_bootstrap_generated.go by scripts/gen-pg-ts-bootstrap.py. Without them
// a PostgreSQL node on a goopg directory has no text search configuration at
// all (to_tsvector('simple', …) fails). The snowball dictionaries/configs a PG
// initdb adds afterwards need its dict_snowball C functions (pg_proc 13212/13213)
// and are not seeded.

type pgTSParserRow struct {
	OID                                       uint32
	Name                                      string
	Nsp, Start, Token, End, Headline, LexType uint32
}

type pgTSTemplateRow struct {
	OID               uint32
	Name              string
	Nsp, Init, Lexize uint32
}

type pgTSDictRow struct {
	OID                  uint32
	Name                 string
	Nsp, Owner, Template uint32
}

type pgTSConfigRow struct {
	OID                uint32
	Name               string
	Nsp, Owner, Parser uint32
}

type pgTSConfigMapRow struct {
	Cfg            uint32
	TokenType, Seq int32
	Dict           uint32
}

type tsNamedRow struct {
	oid  uint32
	name string
	nsp  uint32
}

func bootstrapPgTS(dataDir string) error {
	col := func(n, t string) catalog.Column { return catalog.Column{Name: n, Type: catalog.Type{Name: t}} }
	i := func(v uint32) executor.Datum { return executor.NewIntDatum(int64(v)) }
	s := executor.NewStringDatum
	type rel struct {
		file          string
		cols          []catalog.Column
		rows          []executor.Row
		named         []tsNamedRow
		oidIdx, nmIdx uint32
	}
	var rels []rel

	var r rel
	r = rel{file: "3601", oidIdx: 3607, nmIdx: 3606, cols: []catalog.Column{col("oid", "oid"), col("prsname", "name"), col("prsnamespace", "oid"),
		col("prsstart", "regproc"), col("prstoken", "regproc"), col("prsend", "regproc"), col("prsheadline", "regproc"), col("prslextype", "regproc")}}
	for _, p := range pgTSParserBKI {
		r.rows = append(r.rows, executor.Row{i(p.OID), s(p.Name), i(p.Nsp), i(p.Start), i(p.Token), i(p.End), i(p.Headline), i(p.LexType)})
		r.named = append(r.named, tsNamedRow{p.OID, p.Name, p.Nsp})
	}
	rels = append(rels, r)

	r = rel{file: "3764", oidIdx: 3767, nmIdx: 3766, cols: []catalog.Column{col("oid", "oid"), col("tmplname", "name"), col("tmplnamespace", "oid"),
		col("tmplinit", "regproc"), col("tmpllexize", "regproc")}}
	for _, t := range pgTSTemplateBKI {
		r.rows = append(r.rows, executor.Row{i(t.OID), s(t.Name), i(t.Nsp), i(t.Init), i(t.Lexize)})
		r.named = append(r.named, tsNamedRow{t.OID, t.Name, t.Nsp})
	}
	rels = append(rels, r)

	r = rel{file: "3600", oidIdx: 3605, nmIdx: 3604, cols: []catalog.Column{col("oid", "oid"), col("dictname", "name"), col("dictnamespace", "oid"),
		col("dictowner", "oid"), col("dicttemplate", "oid"), col("dictinitoption", "text")}}
	for _, d := range pgTSDictBKI {
		r.rows = append(r.rows, executor.Row{i(d.OID), s(d.Name), i(d.Nsp), i(d.Owner), i(d.Template), executor.NullDatum})
		r.named = append(r.named, tsNamedRow{d.OID, d.Name, d.Nsp})
	}
	rels = append(rels, r)

	r = rel{file: "3602", oidIdx: 3712, nmIdx: 3608, cols: []catalog.Column{col("oid", "oid"), col("cfgname", "name"), col("cfgnamespace", "oid"),
		col("cfgowner", "oid"), col("cfgparser", "oid")}}
	for _, c := range pgTSConfigBKI {
		r.rows = append(r.rows, executor.Row{i(c.OID), s(c.Name), i(c.Nsp), i(c.Owner), i(c.Parser)})
		r.named = append(r.named, tsNamedRow{c.OID, c.Name, c.Nsp})
	}
	rels = append(rels, r)

	for _, r := range rels {
		tids, err := writeMultiPageHeapRows(dataDir, r.file, r.cols, r.rows)
		if err != nil {
			return fmt.Errorf("%s heap: %w", r.file, err)
		}
		oidT := make([]oidKeyedTID, len(r.named))
		for k, n := range r.named {
			oidT[k] = oidKeyedTID{n.oid, tids[k].Block, tids[k].Offset}
		}
		img, err := buildOidIndexFile(oidT)
		if err != nil {
			return fmt.Errorf("index %d: %w", r.oidIdx, err)
		}
		if err := writeLocalIndexFile(dataDir, r.oidIdx, img); err != nil {
			return err
		}
		type nk struct {
			n   tsNamedRow
			tid heapTID
		}
		ns := make([]nk, len(r.named))
		for k := range r.named {
			ns[k] = nk{r.named[k], tids[k]}
		}
		sort.Slice(ns, func(a, b int) bool {
			if ns[a].n.name != ns[b].n.name {
				return ns[a].n.name < ns[b].n.name
			}
			return ns[a].n.nsp < ns[b].n.nsp
		})
		tuples := make([][]byte, len(ns))
		for k, x := range ns {
			tuples[k] = pgBuildIndexTupleNameOidKey(x.tid.Block, x.tid.Offset, x.n.name, x.n.nsp)
		}
		nimg, err := pgBuildBtreeBulkLoadSized(tuples, 80, 2)
		if err != nil {
			return fmt.Errorf("index %d: %w", r.nmIdx, err)
		}
		if err := writeLocalIndexFile(dataDir, r.nmIdx, nimg); err != nil {
			return err
		}
	}

	// pg_ts_config_map (3603), index 3609 on (mapcfg, maptokentype, mapseqno).
	mapCols := []catalog.Column{col("mapcfg", "oid"), col("maptokentype", "int4"), col("mapseqno", "int4"), col("mapdict", "oid")}
	mapRows := make([]executor.Row, len(pgTSConfigMapBKI))
	for k, m := range pgTSConfigMapBKI {
		mapRows[k] = executor.Row{i(m.Cfg), executor.NewIntDatum(int64(m.TokenType)), executor.NewIntDatum(int64(m.Seq)), i(m.Dict)}
	}
	tids, err := writeMultiPageHeapRows(dataDir, "3603", mapCols, mapRows)
	if err != nil {
		return fmt.Errorf("pg_ts_config_map heap: %w", err)
	}
	type mk struct {
		m   pgTSConfigMapRow
		tid heapTID
	}
	ms := make([]mk, len(pgTSConfigMapBKI))
	for k := range pgTSConfigMapBKI {
		ms[k] = mk{pgTSConfigMapBKI[k], tids[k]}
	}
	sort.Slice(ms, func(a, b int) bool {
		x, y := ms[a].m, ms[b].m
		if x.Cfg != y.Cfg {
			return x.Cfg < y.Cfg
		}
		if x.TokenType != y.TokenType {
			return x.TokenType < y.TokenType
		}
		return x.Seq < y.Seq
	})
	tuples := make([][]byte, len(ms))
	for k, x := range ms {
		tuples[k] = pgBuildIndexTupleOidKeys(x.tid.Block, x.tid.Offset, []uint32{x.m.Cfg, uint32(x.m.TokenType), uint32(x.m.Seq)})
	}
	img, err := pgBuildBtreeBulkLoadSized(tuples, len(tuples[0]), 3)
	if err != nil {
		return fmt.Errorf("index 3609: %w", err)
	}
	return writeLocalIndexFile(dataDir, 3609, img)
}
