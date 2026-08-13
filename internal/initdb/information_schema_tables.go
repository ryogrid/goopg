// information_schema data tables — M0133-S3.
//
// sql_features / sql_sizing / sql_implementation_info / sql_parts are the four
// ORDINARY heap tables information_schema.sql creates at its tail and that
// upstream initdb populates with real rows (initdb.c setup_schema: COPY from
// sql_features.txt + the INSERT/UPDATE statements in information_schema.sql).
// This is the first bulk *data* load goopg performs at initdb — every prior
// bootstrap heap is a metadata catalog. The 801 rows are captured verbatim
// from a fresh PG 18.3 (the "measure, don't read a .dat" rule S1/S2 used) and
// embedded as TSV rather than regenerated.
//
// See docs/design/0133-0003-information-schema-data-tables.md for the object
// graph (each table is five OIDs: table + array type + composite type + toast
// heap + toast index) and the rationale for the base-type-vs-domain split
// below.

package initdb

import (
	"embed"
	"fmt"
	"strconv"
	"strings"

	"github.com/goopg/goopg/internal/catalog"
	"github.com/goopg/goopg/internal/executor"
)

//go:embed sql_features.tsv sql_sizing.tsv sql_implementation_info.tsv sql_parts.tsv
var informationSchemaDataFS embed.FS

// The three information_schema DOMAINS (M0133-S1) the tables' columns use.
const (
	infoSchemaNamespaceOID      uint32 = 13273 // the information_schema namespace (M0133-S1)
	infoSchemaCardinalNumberOID uint32 = 13287 // domain over int4
	infoSchemaCharacterDataOID  uint32 = 13290 // domain over varchar, COLLATE "C"
	infoSchemaYesOrNoOID        uint32 = 13300 // domain over varchar(3), COLLATE "C"
)

// infoSchemaCol is one column of one information_schema data table. TypeOID is
// the DOMAIN OID carried by pg_attribute; BaseType is the base-type name the
// heap ENCODER uses — a domain stores its base type's binary representation and
// EncodeRowPG has no domain cases, so the two lists are decoupled by
// construction (0133-0003 §5). Coll is the attcollation (950 for the C-collated
// varlena domains, else 0).
type infoSchemaCol struct {
	name     string
	typeOID  uint32
	baseType string
	coll     uint32
}

// infoSchemaTable is one information_schema data table with its column list.
type infoSchemaTable struct {
	oid     uint32
	relname string
	reltype uint32 // composite rowtype OID (oid+2)
	cols    []infoSchemaCol
}

// infoSchemaTables lists the four tables in information_schema.sql creation
// order (hence ascending OID), measured against a fresh PG 18.3. Each CREATE
// TABLE consumes five post-bootstrap OIDs: table T, array type T+1, composite
// type T+2, toast heap T+3, toast index T+4.
func infoSchemaTables() []infoSchemaTable {
	return []infoSchemaTable{
		{13456, "sql_features", 13458, []infoSchemaCol{
			{"feature_id", infoSchemaCharacterDataOID, "text", 950},
			{"feature_name", infoSchemaCharacterDataOID, "text", 950},
			{"sub_feature_id", infoSchemaCharacterDataOID, "text", 950},
			{"sub_feature_name", infoSchemaCharacterDataOID, "text", 950},
			{"is_supported", infoSchemaYesOrNoOID, "text", 950},
			{"is_verified_by", infoSchemaCharacterDataOID, "text", 950},
			{"comments", infoSchemaCharacterDataOID, "text", 950},
		}},
		{13461, "sql_implementation_info", 13463, []infoSchemaCol{
			{"implementation_info_id", infoSchemaCharacterDataOID, "text", 950},
			{"implementation_info_name", infoSchemaCharacterDataOID, "text", 950},
			{"integer_value", infoSchemaCardinalNumberOID, "int4", 0},
			{"character_value", infoSchemaCharacterDataOID, "text", 950},
			{"comments", infoSchemaCharacterDataOID, "text", 950},
		}},
		{13466, "sql_parts", 13468, []infoSchemaCol{
			{"feature_id", infoSchemaCharacterDataOID, "text", 950},
			{"feature_name", infoSchemaCharacterDataOID, "text", 950},
			{"is_supported", infoSchemaYesOrNoOID, "text", 950},
			{"is_verified_by", infoSchemaCharacterDataOID, "text", 950},
			{"comments", infoSchemaCharacterDataOID, "text", 950},
		}},
		{13471, "sql_sizing", 13473, []infoSchemaCol{
			{"sizing_id", infoSchemaCardinalNumberOID, "int4", 0},
			{"sizing_name", infoSchemaCharacterDataOID, "text", 950},
			{"supported_value", infoSchemaCardinalNumberOID, "int4", 0},
			{"comments", infoSchemaCharacterDataOID, "text", 950},
		}},
	}
}

// informationSchemaDataTableRels renders the four tables as nailedRel rows so
// they flow into the pg_class / pg_attribute heaps, the pg_type composite rows,
// and the pg_class_relname_nsp_index — WITHOUT entering pg_internal.init.
//
// It is deliberately a THIRD list, neither nailedLocalRels (which would drag
// the tables into the relcache init file — upstream never nails
// information_schema relations) nor nailedToastRels() (whose rels carry
// RelType 0 and are excluded from the pg_type bootstrap). The tables carry a
// real RelType and are wired at exactly the five sites that produce on-disk
// catalog content (see the design doc §1); the toast pairs ride nailedToastPairs
// separately.
func informationSchemaDataTableRels() []nailedRel {
	tables := infoSchemaTables()
	rels := make([]nailedRel, 0, len(tables))
	for _, t := range tables {
		attrs := make([]nailedAttr, len(t.cols))
		for i, c := range t.cols {
			attrs[i] = nailedAttr{
				Name:      c.name,
				TypeOID:   c.typeOID,
				Num:       int16(i + 1),
				Len:       infoSchemaTypeLen(c.typeOID),
				NotNull:   false,
				Collation: c.coll,
			}
		}
		rels = append(rels, nailedRel{
			OID:      t.oid,
			RelName:  t.relname,
			RelType:  t.reltype,
			RelKind:  'r',
			RelNatts: int16(len(t.cols)),
			IsShared: false,
			Attrs:    attrs,
		})
	}
	return rels
}

// infoSchemaTypeLen returns the attlen a domain-typed column carries: int4 for
// cardinal_number, -1 (varlena) for the varchar-backed domains.
func infoSchemaTypeLen(typeOID uint32) int16 {
	if typeOID == infoSchemaCardinalNumberOID {
		return 4
	}
	return -1
}

// infoSchemaTableCols returns the encoder-side column list for one table, using
// base type names so EncodeRowPG can encode domain-typed values by their base
// type's binary representation (0133-0003 §5).
func infoSchemaTableCols(t infoSchemaTable) []catalog.Column {
	cols := make([]catalog.Column, len(t.cols))
	for i, c := range t.cols {
		cols[i] = catalog.Column{Name: c.name, Type: catalog.Type{Name: c.baseType}}
	}
	return cols
}

// infoSchemaTableRows parses the embedded TSV capture for one table into
// executor.Rows. The capture is the raw COPY TO STDOUT of the oracle table:
// tab-separated, `\N` for NULL, `is_verified_by` NULL on every sql_features
// row (upstream's COPY omits it).
func infoSchemaTableRows(t infoSchemaTable) ([]executor.Row, error) {
	raw, err := informationSchemaDataFS.ReadFile(t.relname + ".tsv")
	if err != nil {
		return nil, err
	}
	lines := strings.Split(strings.TrimSuffix(string(raw), "\n"), "\n")
	rows := make([]executor.Row, 0, len(lines))
	for _, line := range lines {
		if line == "" {
			continue
		}
		fields := strings.Split(line, "\t")
		if len(fields) != len(t.cols) {
			return nil, fmt.Errorf("information_schema.%s: row has %d fields, want %d",
				t.relname, len(fields), len(t.cols))
		}
		row := make(executor.Row, len(t.cols))
		for i, c := range t.cols {
			v := fields[i]
			if v == `\N` {
				row[i] = executor.NullDatum
				continue
			}
			if c.baseType == "int4" {
				n, err := strconv.ParseInt(v, 10, 32)
				if err != nil {
					return nil, fmt.Errorf("information_schema.%s: bad int4 %q: %w", t.relname, v, err)
				}
				row[i] = executor.NewIntDatum(n)
			} else {
				row[i] = executor.NewStringDatum(v)
			}
		}
		rows = append(rows, row)
	}
	return rows, nil
}

// bootstrapInformationSchemaDataTables writes the four data heaps into base/1
// and base/5 (template1 + postgres; template0 is re-materialised from base/5
// afterwards by M0131-S15). relfilenode == table OID, so the physical file is
// named by the OID. Runs after bootstrapPgClassTuples / bootstrapPgAttributeTuples /
// bootstrapPgTypeTuples so the tables' catalog rows are already on disk.
func bootstrapInformationSchemaDataTables(dataDir string) error {
	for _, t := range infoSchemaTables() {
		rows, err := infoSchemaTableRows(t)
		if err != nil {
			return err
		}
		if _, err := writeMultiPageHeapRows(dataDir, strconv.FormatUint(uint64(t.oid), 10), infoSchemaTableCols(t), rows); err != nil {
			return fmt.Errorf("information_schema.%s: %w", t.relname, err)
		}
	}
	return nil
}
