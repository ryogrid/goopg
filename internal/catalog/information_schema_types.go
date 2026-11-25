package catalog

import "strings"

// The information_schema domains and their array types (information_schema.sql,
// OIDs as initdb's pg_type bootstrap seeds them, namespace 13273). goopg has
// no per-schema type namespace for built-in objects, so regtype input and
// output resolve these names here. M0149-0001h1a85a.
var informationSchemaTypeOIDs = map[string]uint32{
	"cardinal_number":   13287,
	"_cardinal_number":  13286,
	"character_data":    13290,
	"_character_data":   13289,
	"sql_identifier":    13292,
	"_sql_identifier":   13291,
	"time_stamp":        13298,
	"_time_stamp":       13297,
	"yes_or_no":         13300,
	"_yes_or_no":        13299,
	"cardinal_number[]": 13286,
	"character_data[]":  13289,
	"sql_identifier[]":  13291,
	"time_stamp[]":      13297,
	"yes_or_no[]":       13299,
}

// InformationSchemaTypeOID returns the OID of an information_schema domain (or
// its array type) by typname or `name[]` spelling.
func InformationSchemaTypeOID(name string) (uint32, bool) {
	oid, ok := informationSchemaTypeOIDs[strings.ToLower(strings.ReplaceAll(name, " ", ""))]
	return oid, ok
}

// InformationSchemaTypeName returns format_type's spelling of an
// information_schema domain or array OID without its schema: `sql_identifier`
// or `sql_identifier[]`.
func InformationSchemaTypeName(oid uint32) (string, bool) {
	for name, o := range informationSchemaTypeOIDs {
		if o == oid && !strings.HasPrefix(name, "_") {
			return name, true
		}
	}
	return "", false
}
