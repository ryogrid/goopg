package executor

import (
	"bufio"
	"encoding/binary"
	"errors"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// The permanent statistics file (utils/activity/pgstat.c). goopg writes it in
// PostgreSQL 18's exact layout, so PG reads a goopg data directory's file and
// goopg reads PG's: an int32 format id, one 'F' chunk per fixed-numbered kind
// that is written to file, one 'S' chunk per variable-numbered entry
// (PgStat_HashKey + the kind's shared data), then 'E' and end of file. Sizes
// were measured against the PG 18.3 headers. M0149-0007, design
// docs/design/0100-0149/0149-0007-stats-restart.md.
const (
	pgstatFileFormatID     = 0x01A5BCB7 // PGSTAT_FILE_FORMAT_ID
	pgstatStatFile         = "pg_stat/pgstat.stat"
	pgstatStatTmpFile      = "pg_stat/pgstat.tmp"
	pgstatFileEntryEnd     = 'E'
	pgstatFileEntryFixed   = 'F'
	pgstatFileEntryName    = 'N'
	pgstatFileEntryHash    = 'S'
	pgstatKindDatabase     = 1
	pgstatKindRelation     = 2
	pgstatKindFunction     = 3
	pgstatKindReplSlot     = 4
	pgstatKindSubscription = 5
	pgstatKindArchiver     = 7
	pgstatKindBgwriter     = 8
	pgstatKindCheckpointer = 9
	pgstatKindIO           = 10
	pgstatKindSLRU         = 11
	pgstatKindWAL          = 12
	pgstatDBEntryLen       = 264 // sizeof(PgStat_StatDBEntry)
	pgstatTabEntryLen      = 216 // sizeof(PgStat_StatTabEntry)
	pgstatFuncEntryLen     = 24  // sizeof(PgStat_StatFuncEntry)
	pgstatReplSlotEntryLen = 72  // sizeof(PgStat_StatReplSlotEntry)
	pgstatSubEntryLen      = 80  // sizeof(PgStat_StatSubEntry)
	pgstatNameDataLen      = 64  // NAMEDATALEN
)

// pgstatFixedKinds lists the fixed-numbered kinds with write_to_file, in kind
// order, with shared_data_len (sizeof the kind's stats struct).
var pgstatFixedKinds = []struct {
	kind uint32
	len  int
}{
	{pgstatKindArchiver, 136},    // PgStat_ArchiverStats
	{pgstatKindBgwriter, 32},     // PgStat_BgWriterStats
	{pgstatKindCheckpointer, 88}, // PgStat_CheckpointerStats
	{pgstatKindIO, 51848},        // PgStat_IO
	{pgstatKindSLRU, 64 * 8},     // PgStat_SLRUStats[SLRU_NUM_ELEMENTS]
	{pgstatKindWAL, 40},          // PgStat_WalStats
}

// Byte offsets of the PgStat_StatTabEntry fields goopg tracks (every field is
// 8 bytes, in declaration order).
const (
	tabOffNumScans        = 0 * 8
	tabOffTuplesReturned  = 2 * 8
	tabOffTuplesInserted  = 4 * 8
	tabOffTuplesUpdated   = 5 * 8
	tabOffTuplesDeleted   = 6 * 8
	tabOffLiveTuples      = 9 * 8
	tabOffDeadTuples      = 10 * 8
	tabOffModSinceAnalyze = 11 * 8
	tabOffInsSinceVacuum  = 12 * 8
	tabOffVacuumCount     = 16 * 8
	dbOffStatReset        = 32 * 8 // PgStat_StatDBEntry.stat_reset_timestamp
)

// dbStatsManager holds the database-wide entries (PGSTAT_KIND_DATABASE) and
// the database each flushed relation and function entry belongs to: the
// relation and function stores key on the object OID alone.
type dbStatsManager struct {
	mu        sync.Mutex
	databases map[uint32]time.Time // dboid → stat_reset_timestamp (zero: never reset)
	relDB     map[uint32]uint32
	funcDB    map[uint32]uint32
}

var dbStats = &dbStatsManager{
	databases: make(map[uint32]time.Time),
	relDB:     make(map[uint32]uint32),
	funcDB:    make(map[uint32]uint32),
}

// noteFlush records that a session in dboid flushed its pending stats: the
// database gets an entry (pgstat_flush_pending_entries reports the database
// too), and each flushed relation and function is attributed to it.
func (m *dbStatsManager) noteFlush(dboid uint32, rels, funcs []uint32) {
	if dboid == 0 {
		return
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, ok := m.databases[dboid]; !ok {
		m.databases[dboid] = time.Time{}
	}
	for _, oid := range rels {
		m.relDB[oid] = dboid
	}
	for _, oid := range funcs {
		m.funcDB[oid] = dboid
	}
}

// haveStats is pg_stat_have_stats(kind, dboid, objid) for the kinds goopg
// keeps entries for.
func haveStats(kind string, dboid uint32, objid uint64) (bool, error) {
	switch kind {
	case "database":
		dbStats.mu.Lock()
		_, ok := dbStats.databases[dboid]
		dbStats.mu.Unlock()
		return ok && objid == 0, nil
	case "relation":
		if _, ok := relStats.get(uint32(objid)); !ok {
			return false, nil
		}
		dbStats.mu.Lock()
		db, ok := dbStats.relDB[uint32(objid)]
		dbStats.mu.Unlock()
		return !ok || db == dboid, nil
	case "function":
		if _, ok := funcStats.get(uint32(objid)); !ok {
			return false, nil
		}
		dbStats.mu.Lock()
		db, ok := dbStats.funcDB[uint32(objid)]
		dbStats.mu.Unlock()
		return !ok || db == dboid, nil
	case "replslot", "subscription", "backend", "archiver", "bgwriter", "checkpointer", "io", "slru", "wal":
		return false, nil
	}
	return false, &ExecError{Code: "22023", Message: `invalid statistics kind: "` + kind + `"`}
}

func timestampTz(t time.Time) int64 {
	if t.IsZero() {
		return 0
	}
	return t.Sub(pgEpoch).Microseconds()
}

func fromTimestampTz(v int64) time.Time {
	if v == 0 {
		return time.Time{}
	}
	return pgEpoch.Add(time.Duration(v) * time.Microsecond)
}

// WritePgStatFile is pgstat_write_statsfile: at a clean shutdown the
// checkpointer writes the cumulative stats to pg_stat/pgstat.stat (through a
// temporary file renamed into place). Failures are logged, as PG does.
func WritePgStatFile(dataDir string) {
	if dataDir == "" {
		return
	}
	tmp := filepath.Join(dataDir, pgstatStatTmpFile)
	final := filepath.Join(dataDir, pgstatStatFile)
	if err := os.MkdirAll(filepath.Dir(tmp), 0o700); err != nil {
		slog.Warn("could not create statistics directory", "err", err)
		return
	}
	f, err := os.OpenFile(tmp, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o600)
	if err != nil {
		slog.Warn(`could not open temporary statistics file "`+tmp+`"`, "err", err)
		return
	}
	w := bufio.NewWriter(f)
	le := binary.LittleEndian
	var u32 [4]byte
	le.PutUint32(u32[:], pgstatFileFormatID)
	w.Write(u32[:])
	for _, fk := range pgstatFixedKinds {
		w.WriteByte(pgstatFileEntryFixed)
		le.PutUint32(u32[:], fk.kind)
		w.Write(u32[:])
		w.Write(make([]byte, fk.len))
	}
	writeKey := func(kind, dboid uint32, objid uint64) {
		var k [16]byte
		le.PutUint32(k[0:], kind)
		le.PutUint32(k[4:], dboid)
		le.PutUint64(k[8:], objid)
		w.WriteByte(pgstatFileEntryHash)
		w.Write(k[:])
	}
	put := func(b []byte, off int, v int64) { le.PutUint64(b[off:], uint64(v)) }

	dbStats.mu.Lock()
	dbs := make(map[uint32]time.Time, len(dbStats.databases))
	for k, v := range dbStats.databases {
		dbs[k] = v
	}
	relDB := make(map[uint32]uint32, len(dbStats.relDB))
	for k, v := range dbStats.relDB {
		relDB[k] = v
	}
	funcDB := make(map[uint32]uint32, len(dbStats.funcDB))
	for k, v := range dbStats.funcDB {
		funcDB[k] = v
	}
	dbStats.mu.Unlock()

	for dboid, reset := range dbs {
		writeKey(pgstatKindDatabase, dboid, 0)
		b := make([]byte, pgstatDBEntryLen)
		put(b, dbOffStatReset, timestampTz(reset))
		w.Write(b)
	}
	relStats.mu.Lock()
	for oid, c := range relStats.shared {
		writeKey(pgstatKindRelation, relDB[oid], uint64(oid))
		b := make([]byte, pgstatTabEntryLen)
		put(b, tabOffNumScans, c.numScans)
		put(b, tabOffTuplesReturned, c.tuplesReturned)
		put(b, tabOffTuplesInserted, c.tuplesInserted)
		put(b, tabOffTuplesUpdated, c.tuplesUpdated)
		put(b, tabOffTuplesDeleted, c.tuplesDeleted)
		put(b, tabOffLiveTuples, c.deltaLive)
		put(b, tabOffDeadTuples, c.deltaDead)
		put(b, tabOffModSinceAnalyze, c.changedTuples)
		put(b, tabOffInsSinceVacuum, c.insSinceVacuum)
		put(b, tabOffVacuumCount, c.vacuumCount)
		w.Write(b)
	}
	relStats.mu.Unlock()
	funcStats.mu.Lock()
	for oid, c := range funcStats.shared {
		writeKey(pgstatKindFunction, funcDB[oid], uint64(oid))
		b := make([]byte, pgstatFuncEntryLen)
		put(b, 0, c.calls)
		put(b, 8, c.totalTime.Microseconds())
		put(b, 16, c.selfTime.Microseconds())
		w.Write(b)
	}
	funcStats.mu.Unlock()
	w.WriteByte(pgstatFileEntryEnd)

	err = w.Flush()
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		slog.Warn(`could not write temporary statistics file "`+tmp+`"`, "err", err)
		_ = os.Remove(tmp)
		return
	}
	if err := os.Rename(tmp, final); err != nil {
		slog.Warn(`could not rename temporary statistics file "`+tmp+`" to "`+final+`"`, "err", err)
		_ = os.Remove(tmp)
	}
}

// errCorruptStatsFile marks any deviation from the expected layout.
var errCorruptStatsFile = errors.New("corrupted statistics file")

// RestorePgStatFile is pgstat_restore_stats → pgstat_read_statsfile: a
// startup that is not a crash recovery loads pg_stat/pgstat.stat and removes
// it. A file that does not parse completely, or has bytes after 'E', is
// discarded with a LOG, and the stats start empty (pgstat_reset_after_failure).
func RestorePgStatFile(dataDir string) {
	if dataDir == "" {
		return
	}
	path := filepath.Join(dataDir, pgstatStatFile)
	data, err := os.ReadFile(path)
	if err != nil {
		if !os.IsNotExist(err) {
			slog.Warn(`could not open statistics file "`+path+`"`, "err", err)
		}
		resetAllCumulativeStats()
		return
	}
	if err := loadPgStatFile(data); err != nil {
		slog.Info(`corrupted statistics file "` + path + `"`)
		resetAllCumulativeStats()
	}
	_ = os.Remove(path)
}

// DiscardPgStatFile is pgstat_discard_stats: crash recovery throws the
// stats away, removing the file.
func DiscardPgStatFile(dataDir string) {
	if dataDir != "" {
		if err := os.Remove(filepath.Join(dataDir, pgstatStatFile)); err != nil && !os.IsNotExist(err) {
			slog.Warn("could not unlink permanent statistics file", "err", err)
		}
	}
	resetAllCumulativeStats()
}

// resetAllCumulativeStats empties the variable-numbered stores.
func resetAllCumulativeStats() {
	relStats.mu.Lock()
	relStats.shared = make(map[uint32]*relStatCounters)
	relStats.mu.Unlock()
	funcStats.mu.Lock()
	funcStats.shared = make(map[uint32]*funcStatCounters)
	funcStats.mu.Unlock()
	dbStats.mu.Lock()
	dbStats.databases = make(map[uint32]time.Time)
	dbStats.relDB = make(map[uint32]uint32)
	dbStats.funcDB = make(map[uint32]uint32)
	dbStats.mu.Unlock()
}

// loadPgStatFile parses a whole statistics file and, only when it is valid,
// installs its entries.
func loadPgStatFile(data []byte) error {
	le := binary.LittleEndian
	r := data
	take := func(n int) ([]byte, error) {
		if len(r) < n {
			return nil, io.ErrUnexpectedEOF
		}
		b := r[:n]
		r = r[n:]
		return b, nil
	}
	b, err := take(4)
	if err != nil || le.Uint32(b) != pgstatFileFormatID {
		return errCorruptStatsFile
	}
	rels := make(map[uint32]*relStatCounters)
	funcs := make(map[uint32]*funcStatCounters)
	dbs := make(map[uint32]time.Time)
	relDB := make(map[uint32]uint32)
	funcDB := make(map[uint32]uint32)
	get := func(b []byte, off int) int64 { return int64(le.Uint64(b[off:])) }
	for {
		t, err := take(1)
		if err != nil {
			return errCorruptStatsFile
		}
		switch t[0] {
		case pgstatFileEntryFixed:
			kb, err := take(4)
			if err != nil {
				return errCorruptStatsFile
			}
			kind := le.Uint32(kb)
			n := -1
			for _, fk := range pgstatFixedKinds {
				if fk.kind == kind {
					n = fk.len
				}
			}
			if n < 0 {
				return errCorruptStatsFile
			}
			if _, err := take(n); err != nil {
				return errCorruptStatsFile
			}
		case pgstatFileEntryHash:
			k, err := take(16)
			if err != nil {
				return errCorruptStatsFile
			}
			kind, dboid, objid := le.Uint32(k[0:]), le.Uint32(k[4:]), le.Uint64(k[8:])
			var n int
			switch kind {
			case pgstatKindDatabase:
				n = pgstatDBEntryLen
			case pgstatKindRelation:
				n = pgstatTabEntryLen
			case pgstatKindFunction:
				n = pgstatFuncEntryLen
			case pgstatKindSubscription:
				n = pgstatSubEntryLen
			default:
				return errCorruptStatsFile
			}
			body, err := take(n)
			if err != nil {
				return errCorruptStatsFile
			}
			switch kind {
			case pgstatKindDatabase:
				dbs[dboid] = fromTimestampTz(get(body, dbOffStatReset))
			case pgstatKindRelation:
				rels[uint32(objid)] = &relStatCounters{
					numScans:       get(body, tabOffNumScans),
					tuplesReturned: get(body, tabOffTuplesReturned),
					tuplesInserted: get(body, tabOffTuplesInserted),
					tuplesUpdated:  get(body, tabOffTuplesUpdated),
					tuplesDeleted:  get(body, tabOffTuplesDeleted),
					deltaLive:      get(body, tabOffLiveTuples),
					deltaDead:      get(body, tabOffDeadTuples),
					changedTuples:  get(body, tabOffModSinceAnalyze),
					insSinceVacuum: get(body, tabOffInsSinceVacuum),
					vacuumCount:    get(body, tabOffVacuumCount),
				}
				relDB[uint32(objid)] = dboid
			case pgstatKindFunction:
				funcs[uint32(objid)] = &funcStatCounters{
					calls:     get(body, 0),
					totalTime: time.Duration(get(body, 8)) * time.Microsecond,
					selfTime:  time.Duration(get(body, 16)) * time.Microsecond,
				}
				funcDB[uint32(objid)] = dboid
			}
		case pgstatFileEntryName:
			// A replication slot entry: kind, NameData, then its data.
			kb, err := take(4)
			if err != nil || le.Uint32(kb) != pgstatKindReplSlot {
				return errCorruptStatsFile
			}
			if _, err := take(pgstatNameDataLen + pgstatReplSlotEntryLen); err != nil {
				return errCorruptStatsFile
			}
		case pgstatFileEntryEnd:
			if len(r) != 0 {
				return errCorruptStatsFile
			}
			relStats.mu.Lock()
			relStats.shared = rels
			relStats.mu.Unlock()
			funcStats.mu.Lock()
			funcStats.shared = funcs
			funcStats.mu.Unlock()
			dbStats.mu.Lock()
			dbStats.databases = dbs
			dbStats.relDB = relDB
			dbStats.funcDB = funcDB
			dbStats.mu.Unlock()
			return nil
		default:
			return errCorruptStatsFile
		}
	}
}
