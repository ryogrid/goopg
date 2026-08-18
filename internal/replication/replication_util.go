// Package replication implements goopg's streaming and logical replication:
// the walsender-side command dispatcher and BASE_BACKUP routing, the physical
// and logical walreceivers, initial table sync, and the subscription apply
// launcher. It mirrors postgres/src/backend/replication/ (walsender.c,
// walreceiver.c, slot.c, syncrep.c and logical/{launcher,tablesync,worker}.c).
//
// File layout follows upstream's split:
//
//   - walsender.go        — primary side: the replication-command dispatcher
//     (exec_replication_command) and the physical streaming loop (walsender.c)
//   - walreceiver.go      — standby side: the streaming client and the
//     reconnecting launcher (walreceiver.c + libpqwalreceiver.c)
//   - logicalwalsender.go — the LOGICAL arm of START_REPLICATION, which hangs
//     off the same Handler (upstream keeps both in walsender.c)
//   - logicalreceiver.go, tablesync*.go, applylauncher.go — the subscriber
//     side (logical/{worker,tablesync,launcher}.c)
//   - replication_util.go — this file: helpers shared across those roles
//
// The dependency direction is postmaster -> replication -> backup, and it must
// stay that way: do NOT import internal/postmaster from here. The handful of
// postmaster helpers this code used to reach — writeQueryError (supplied as a
// callback), extractCString, resolveConnDBOid and the pg_type OID consts —
// are carried locally for exactly that reason. The same rule is why the
// walreceiver launcher takes a narrow WalReceiverLauncherConfig instead of
// initdb.Runtime.
package replication

import (
	"fmt"
	"strconv"
	"strings"
)

// parseLSN parses upstream's `X/X` hex notation back into a uint64.
// Empty / "0/0" → 0 (start of WAL).
func parseLSN(s string) (uint64, error) {
	parts := strings.SplitN(s, "/", 2)
	if len(parts) != 2 {
		return 0, fmt.Errorf("invalid LSN %q (want X/X)", s)
	}
	hi, err := strconv.ParseUint(parts[0], 16, 32)
	if err != nil {
		return 0, fmt.Errorf("invalid LSN %q: %w", s, err)
	}
	lo, err := strconv.ParseUint(parts[1], 16, 32)
	if err != nil {
		return 0, fmt.Errorf("invalid LSN %q: %w", s, err)
	}
	return (hi << 32) | lo, nil
}

// formatLSN renders an LSN in upstream's `X/X` notation. PostgreSQL's
// pg_lsn is an unsigned 64-bit byte position split into two 32-bit
// halves, hex-formatted. Walprotocol payloads carry the value as a
// big-endian uint64; this is just for the human-facing wire columns.
func formatLSN(lsn uint64) string {
	return fmt.Sprintf("%X/%X", uint32(lsn>>32), uint32(lsn))
}

// summariseErrorResponse extracts a human-readable summary from an
// ErrorResponse payload (sequence of (field-byte, NUL-terminated
// string) pairs ending with a terminating zero byte). Used only
// for error messages — full structured access lives in the wire
// dispatcher.
func summariseErrorResponse(payload []byte) string {
	var msg, code string
	i := 0
	for i < len(payload) {
		field := payload[i]
		i++
		if field == 0 {
			break
		}
		end := i
		for end < len(payload) && payload[end] != 0 {
			end++
		}
		val := string(payload[i:end])
		i = end + 1
		switch field {
		case 'M':
			msg = val
		case 'C':
			code = val
		}
	}
	if code != "" && msg != "" {
		return fmt.Sprintf("%s: %s", code, msg)
	}
	if msg != "" {
		return msg
	}
	return "(empty error)"
}
