// information_schema view OID policy (M0133-S4, 2026-08-13).
//
// The same Option-A identity-pinning rule that M0131-S8a applied to the
// pg_catalog system views (docs/design/0131-0008-system-view-oid-policy.md)
// applies unchanged to the information_schema views: goopg pins each view's
// OID and its _RETURN rule OID to what PG 18.3's own initdb assigns while
// executing information_schema.sql. The reasons are identical — a captured
// ev_action is a serialised Query tree that names base relations by OID, and
// information_schema.sql is full of view-on-view chains (16 edges over its 65
// views, measured 2026-08-13), so a dependent view's blob embeds its base
// view's initdb-assigned OID. Under Option A those embedded OIDs are already
// correct; the capture acceptance test is a byte cmp.
//
// The information_schema band is NOT dense. It shares the single post-bootstrap
// counter with everything the pg_catalog corpus consumed, and the objects are
// interleaved by information_schema.sql creation order:
//
//	13273          the information_schema namespace
//	13274..13285   the 11 helper functions (13280 is a hole)  [M0133-S2]
//	13286..13300   the 5 domains + 5 array peers              [M0133-S1]
//	13293..13621   the 65 views (interleaved with the domains)
//	13456..13475   the 4 data tables (5 OIDs each)            [M0133-S3]
//
// Every view's reltype is oid+2 (its composite rowtype) and its _RETURN rule
// is oid+3, exactly the pg_catalog pattern. goopg keeps the M0131-S6.5
// divergence: RelType 2249 (RECORDOID) rather than the composite type.
package initdb

// informationSchemaViewOIDPins returns the pinned table for the information_schema
// views goopg has adopted, in upstream OID order. It reuses systemViewOIDPin
// because the captured columns are identical; the semantic difference is the
// namespace (13273, not 11), which the consumer derives from the OID via
// pgClassRelnamespaceFor.
//
// Tranche 1 (2026-08-13) is the "no-known-blocker" set — the 33 views whose
// ev_action is catalog-direct (no in-band :relid, no in-band :funcid) and
// whose stored ev_action stays under the 8000 B inline budget. Measured against
// a fresh PG 18.3 (the same oracle the pg_catalog table was captured from);
// every reltype/rule OID follows the oid+2/oid+3 law verified for all 65.
//
// Four of the 33 were initially withheld on incomplete goopg catalog
// descriptors, then landed by the descriptor-completion slice: character_sets,
// collations and collation_character_set_applicability read pg_collation
// (3456) columns 9–12, which goopg seeded with 8 columns where PG18 has 12
// (and collcollate as name where PG18 has text); triggers reads pg_trigger
// (2620) columns 3 and 9–19, which goopg seeded with 8 columns where PG18 has
// 19. pgCollationAttrs/pgTriggerAttrs are now the full PG18 schemas, so the
// four evaluate on a hosted PG like the other 29.
//
// Tranche 2 (2026-08-14) is the TOAST set — the ten catalog-direct views whose
// stored ev_action EXCEEDS the 8000 B inline budget, so the M0131-S20.2
// externalisation writer (DECLARE_TOAST(pg_rewrite, 2838, 2839) + chunk
// writer) stores them out of line in pg_toast_2618. Three of the ten
// (attributes, columns, domains) additionally reference the S2 helper
// functions (:funcid 13274..13285), which resolve because S2 landed them; they
// carry no in-band :relid, so they are capturable now and are NOT tranche-3/4
// views. element_types — the eleventh over-budget value — is deferred to
// tranche 4 because it embeds :relid 13553 (data_type_privileges).
//
// Tranche 3 (2026-08-14) is the helper-function set — the four views whose
// ev_action embeds in-band :funcid references to the S2 helpers (13274..13285)
// but no in-band :relid, and whose stored ev_action stays under the 8000 B
// inline budget: key_column_usage, parameters, sequences,
// triggered_update_columns. They are capturable now because S2 landed the
// helpers those funcids resolve to.
//
// Tranche 4 (2026-08-14) is the view-on-view set — the 18 views whose ev_action
// embeds in-band :relid references to OTHER information_schema views (the
// "16 view-on-view edges" of design 0133-0004). Their bases — applicable_roles,
// enabled_roles, the column/routine/table/udt/usage_privileges family, the
// attributes/columns/domains/parameters/routines family, and the _pg_foreign_*
// leaf set — all landed in tranches 1–3, so every embedded :relid resolves once
// these 18 are themselves pinned. One of them (element_types, 10956 B stored)
// exceeds the 8000 B inline budget and is the eleventh F33 value; the other 17
// store inline.
func informationSchemaViewOIDPins() []systemViewOIDPin {
	return []systemViewOIDPin{
		{"information_schema_catalog_name", 13293, 13296, 13295, 1},
		{"applicable_roles", 13302, 13305, 13304, 3},
		{"administrable_role_authorizations", 13307, 13310, 13309, 3},
		{"attributes", 13311, 13314, 13313, 31},
		{"character_sets", 13316, 13319, 13318, 8},
		{"check_constraint_routine_usage", 13321, 13324, 13323, 6},
		{"check_constraints", 13326, 13329, 13328, 4},
		{"collations", 13331, 13334, 13333, 4},
		{"collation_character_set_applicability", 13336, 13339, 13338, 6},
		{"column_column_usage", 13341, 13344, 13343, 5},
		{"column_domain_usage", 13346, 13349, 13348, 7},
		{"column_privileges", 13351, 13354, 13353, 8},
		{"column_udt_usage", 13356, 13359, 13358, 7},
		{"columns", 13361, 13364, 13363, 44},
		{"constraint_column_usage", 13366, 13369, 13368, 7},
		{"constraint_table_usage", 13371, 13374, 13373, 6},
		{"domain_constraints", 13376, 13379, 13378, 8},
		{"domain_udt_usage", 13381, 13384, 13383, 6},
		{"domains", 13385, 13388, 13387, 27},
		{"enabled_roles", 13390, 13393, 13392, 1},
		{"key_column_usage", 13394, 13397, 13396, 9},
		{"parameters", 13399, 13402, 13401, 32},
		{"referential_constraints", 13404, 13407, 13406, 9},
		{"role_column_grants", 13409, 13412, 13411, 8},
		{"routine_column_usage", 13413, 13416, 13415, 10},
		{"routine_privileges", 13418, 13421, 13420, 10},
		{"role_routine_grants", 13423, 13426, 13425, 10},
		{"routine_routine_usage", 13427, 13430, 13429, 6},
		{"routine_sequence_usage", 13432, 13435, 13434, 9},
		{"routine_table_usage", 13437, 13440, 13439, 9},
		{"routines", 13442, 13445, 13444, 82},
		{"schemata", 13447, 13450, 13449, 7},
		{"sequences", 13451, 13454, 13453, 12},
		{"table_constraints", 13476, 13479, 13478, 11},
		{"table_privileges", 13481, 13484, 13483, 8},
		{"role_table_grants", 13486, 13489, 13488, 8},
		{"tables", 13490, 13493, 13492, 12},
		{"transforms", 13495, 13498, 13497, 8},
		{"triggered_update_columns", 13500, 13503, 13502, 7},
		{"triggers", 13505, 13508, 13507, 17},
		{"udt_privileges", 13510, 13513, 13512, 7},
		{"role_udt_grants", 13515, 13518, 13517, 7},
		{"usage_privileges", 13519, 13522, 13521, 8},
		{"role_usage_grants", 13524, 13527, 13526, 8},
		{"user_defined_types", 13528, 13531, 13530, 29},
		{"view_column_usage", 13533, 13536, 13535, 7},
		{"view_routine_usage", 13538, 13541, 13540, 6},
		{"view_table_usage", 13543, 13546, 13545, 6},
		{"views", 13548, 13551, 13550, 10},
		{"data_type_privileges", 13553, 13556, 13555, 5},
		{"element_types", 13558, 13561, 13560, 28},
		{"_pg_foreign_table_columns", 13563, 13566, 13565, 4},
		{"column_options", 13568, 13571, 13570, 6},
		{"_pg_foreign_data_wrappers", 13572, 13575, 13574, 7},
		{"foreign_data_wrapper_options", 13576, 13579, 13578, 4},
		{"foreign_data_wrappers", 13580, 13583, 13582, 5},
		{"_pg_foreign_servers", 13584, 13587, 13586, 9},
		{"foreign_server_options", 13588, 13591, 13590, 4},
		{"foreign_servers", 13592, 13595, 13594, 7},
		{"_pg_foreign_tables", 13596, 13599, 13598, 7},
		{"foreign_table_options", 13601, 13604, 13603, 5},
		{"foreign_tables", 13605, 13608, 13607, 5},
		{"_pg_user_mappings", 13609, 13612, 13611, 7},
		{"user_mapping_options", 13614, 13617, 13616, 5},
		{"user_mappings", 13619, 13622, 13621, 3},
	}
}
