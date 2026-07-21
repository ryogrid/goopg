package catalog

// OperatorEntry holds one row of pg_operator.dat (PG18). The generated table
// PGOperatorAllEntries (pg_operator_seed_data.go) returns the full 799-row set;
// initdb builds pg_operator heap rows from it at bootstrap, and the node-tree
// resolver (02e item C) builds a forward (name, operand-OIDs) → operator index
// from it to emit canonical OpExpr nodes. Field names match pg_operator.h.
//
// Relocated from internal/initdb (02e item C S0) so a leaf resolver package can
// reach the data without importing initdb.
type OperatorEntry struct {
	OID        uint32
	Name       string // oprname
	Namespace  uint32 // oprnamespace
	Owner      uint32 // oprowner
	Kind       byte   // oprkind: 'b'=binary, 'l'=left-unary
	CanMerge   bool   // oprcanmerge
	CanHash    bool   // oprcanhash
	LeftType   uint32 // oprleft
	RightType  uint32 // oprright
	ResultType uint32 // oprresult
	Commutator uint32 // oprcom
	Negator    uint32 // oprnegate
	Code       uint32 // oprcode (pg_proc OID)
	Restrict   uint32 // oprrest (pg_proc OID)
	Join       uint32 // oprjoin (pg_proc OID)
}
