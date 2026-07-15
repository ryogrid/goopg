package wal

// GUCParameters holds the 8 GUC fields that PostgreSQL broadcasts via
// XLOG_PARAMETER_CHANGE so standbys can enforce compatibility via
// CheckRequiredParameterValues (src/backend/access/transam/xlog.c).
// Mirrors `xl_parameter_change` in src/include/access/xlog_internal.h.
//
// The GUC echo is retained because the checkpointer stamps these values
// into pg_control; the XLOG_PARAMETER_CHANGE WAL record itself is no
// longer emitted (it was a canonical-only record, removed with the
// native→PG-dispatch change — see docs/design/wal-native-pg-format/04-*).
type GUCParameters struct {
	MaxConnections     int32
	MaxWorkerProcesses int32
	MaxWalSenders      int32
	MaxPreparedXacts   int32
	MaxLocksPerXact    int32
	// WalLevel: 0=minimal, 1=replica, 2=logical (matches PG's WalLevel enum).
	WalLevel             int32
	WalLogHints          bool
	TrackCommitTimestamp bool
}

// DefaultGUCParameters returns the standard GUC values for a goopg primary
// that matches a PG18 initdb configuration.
func DefaultGUCParameters() GUCParameters {
	return GUCParameters{
		MaxConnections:       100,
		MaxWorkerProcesses:   8,
		MaxWalSenders:        10,
		MaxPreparedXacts:     0,
		MaxLocksPerXact:      64,
		WalLevel:             1, // replica — minimum for streaming replication
		WalLogHints:          false,
		TrackCommitTimestamp: false,
	}
}
