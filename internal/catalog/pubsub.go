package catalog

import (
	"errors"
	"fmt"
	"sort"
	"sync"
)

// Publication is one CREATE PUBLICATION's catalog row. Mirrors
// the upstream pg_publication shape closely enough that operators
// who already know `\dRp` can read the goopg view without
// surprises. See
// docs/design/0008-0003-publication-subscription-ddl.md.
type Publication struct {
	Name string
	OID  uint32
	// Owner is the owning role's OID (pg_publication.pubowner). Real
	// pg_dump's getPublications() (pg_dump.c) always selects this column
	// and calls getRoleName() on it, which pg_fatal()s with "role with
	// OID %u does not exist" if the OID doesn't resolve — so an unset
	// (zero) Owner isn't a cosmetic gap, it makes pg_dump abort outright
	// on any database containing a publication. CreatePublication defaults
	// this to the bootstrap superuser OID (10); CreatePublicationAsOwner
	// (called from the executor with the connection's currently-effective
	// role, DU-002 slice 424) lets a session running under `SET ROLE`/`SET
	// SESSION AUTHORIZATION` own the publication it creates, mirroring
	// PostgreSQL's `GetUserId()`-as-owner convention (`CreatePublication`,
	// publicationcmds.c). DU-002 slice 422.
	Owner         uint32
	AllTables     bool
	PublishInsert bool
	PublishUpdate bool
	PublishDelete bool
	// Tables are qualified table names ("schema.name") the
	// publication is restricted to. Ignored when AllTables is
	// true. Insertion order is preserved so views render rows
	// in the order an operator added them.
	Tables []string
}

// Subscription is one CREATE SUBSCRIPTION's catalog row.
type Subscription struct {
	Name     string
	OID      uint32
	Conninfo string
	// Owner is the owning role's OID (pg_subscription.subowner). Real
	// pg_dump's getSubscriptions() (pg_dump.c) always selects this column
	// and calls getRoleName() on it — same pg_fatal()-on-unresolved-OID
	// hazard pg_publication.pubowner had (DU-002 slice 422). CreateSubscription
	// defaults this to the bootstrap superuser OID (10);
	// CreateSubscriptionAsOwner (called from the executor with the
	// connection's currently-effective role, DU-002 slice 424) mirrors
	// Publication.Owner's same real-ownership convention.
	Owner        uint32
	Publications []string
	Enabled      bool
	SlotName     string
}

// SubscriptionRel is one tablesync row: a (subscription, relation)
// pair plus its current sync state. Mirrors upstream
// pg_subscription_rel's `srsubid` / `srrelid` / `srsubstate` /
// `srsublsn` columns. State values follow upstream's letter codes:
//
//	'i' — initial (waiting for sync to start)
//	'd' — data is being copied
//	's' — synchronized (sync done, catching up to streaming)
//	'r' — ready (joined the streaming apply path)
//
// Mirrors upstream's `SUBREL_STATE_*` constants in
// postgres/src/include/catalog/pg_subscription_rel.h. See
// docs/design/0008-0004-apply-worker-and-tablesync.md.
type SubscriptionRel struct {
	SubID  uint32
	RelOID uint32
	State  string
	LSN    uint64
}

// Tablesync state letters matching upstream's
// `SUBREL_STATE_INIT` / `_DATASYNC` / `_SYNCDONE` / `_READY`.
const (
	SubRelStateInit     = "i"
	SubRelStateDataCopy = "d"
	SubRelStateSyncDone = "s"
	SubRelStateReady    = "r"
)

// Tablesync errors surfaced by the state-machine API.
var (
	ErrSubscriptionRelExists       = errors.New("subscription_rel already exists")
	ErrSubscriptionRelNotFound     = errors.New("subscription_rel does not exist")
	ErrInvalidSubRelState          = errors.New("invalid subscription_rel state")
	ErrInvalidSubRelStateTransition = errors.New("invalid subscription_rel state transition")
)

// Errors surfaced by PubSub mutators.
var (
	ErrPublicationExists    = errors.New("publication already exists")
	ErrPublicationNotFound  = errors.New("publication does not exist")
	ErrSubscriptionExists   = errors.New("subscription already exists")
	ErrSubscriptionNotFound = errors.New("subscription does not exist")
)

// PubSub is the in-memory registry for publications and
// subscriptions. Single-process; mutations and reads are
// serialised by an internal RWMutex. Construct one per goopg
// runtime via NewPubSub.
type PubSub struct {
	mu            sync.RWMutex
	publications  map[string]*Publication
	subscriptions map[string]*Subscription
	// subRels is the tablesync state-machine store. Keyed by
	// subscription name → RelOID → SubscriptionRel. The
	// per-subscription map keeps lookups + iteration cheap and
	// matches the way pg_subscription_rel is queried in
	// practice (always filtered by srsubid).
	subRels map[string]map[uint32]*SubscriptionRel
	// nextOID is local to the registry. PubSub OIDs share the
	// same numeric space upstream uses (anything ≥ FirstUserOID),
	// but they don't collide with user-table OIDs because the
	// catalog and the registry allocate independently — no
	// goopg surface joins their OID columns yet, and the M0008
	// DoD cares about names, not exact OIDs.
	nextOID uint32
}

// NewPubSub constructs an empty registry.
func NewPubSub() *PubSub {
	return &PubSub{
		publications:  map[string]*Publication{},
		subscriptions: map[string]*Subscription{},
		subRels:       map[string]map[uint32]*SubscriptionRel{},
		nextOID:       FirstUserOID,
	}
}

// PublicationOptions controls the optional fields of a
// CreatePublication call. Zero values match the upstream
// `CREATE PUBLICATION` defaults: insert/update/delete/truncate
// publishing are all on; row-filter / column-list / via-root
// are off.
type PublicationOptions struct {
	AllTables     bool
	PublishInsert bool
	PublishUpdate bool
	PublishDelete bool
}

// DefaultPublicationOptions returns the upstream-default option
// set: publish=insert,update,delete (truncate is M0008-out-of-
// scope so it stays off).
func DefaultPublicationOptions() PublicationOptions {
	return PublicationOptions{
		AllTables:     false,
		PublishInsert: true,
		PublishUpdate: true,
		PublishDelete: true,
	}
}

// CreatePublication registers a new publication owned by the bootstrap
// superuser. tables is the qualified-table-name list (`"schema.name"`); pass
// nil for FOR ALL TABLES (with opts.AllTables = true) or for an empty FOR
// TABLE list. Returns ErrPublicationExists when name is taken. Callers that
// know the connection's currently-effective role should use
// CreatePublicationAsOwner instead.
func (p *PubSub) CreatePublication(name string, tables []string, opts PublicationOptions) (*Publication, error) {
	return p.CreatePublicationAsOwner(name, tables, opts, 10)
}

// CreatePublicationAsOwner is CreatePublication with an explicit owner OID —
// the role that issued CREATE PUBLICATION, mirroring PostgreSQL's
// GetUserId()-as-owner convention (CreatePublication, publicationcmds.c).
// DU-002 slice 424.
func (p *PubSub) CreatePublicationAsOwner(name string, tables []string, opts PublicationOptions, owner uint32) (*Publication, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if _, ok := p.publications[name]; ok {
		return nil, ErrPublicationExists
	}
	pub := &Publication{
		Name:          name,
		OID:           p.nextOID,
		Owner:         owner,
		AllTables:     opts.AllTables,
		PublishInsert: opts.PublishInsert,
		PublishUpdate: opts.PublishUpdate,
		PublishDelete: opts.PublishDelete,
	}
	if !opts.AllTables && len(tables) > 0 {
		pub.Tables = append(pub.Tables, tables...)
	}
	p.nextOID++
	p.publications[name] = pub
	out := *pub
	out.Tables = append([]string(nil), pub.Tables...)
	return &out, nil
}

// SetPublicationOwner updates a publication's owner OID. Returns
// ErrPublicationNotFound when name is unknown. Backs ALTER PUBLICATION name
// OWNER TO newowner. DU-002 slice 425 (M0119-0004, loop #65 ledger
// follow-up).
func (p *PubSub) SetPublicationOwner(name string, owner uint32) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	pub, ok := p.publications[name]
	if !ok {
		return ErrPublicationNotFound
	}
	pub.Owner = owner
	return nil
}

// DropPublication removes a publication. Returns
// ErrPublicationNotFound when name is unknown.
func (p *PubSub) DropPublication(name string) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if _, ok := p.publications[name]; !ok {
		return ErrPublicationNotFound
	}
	delete(p.publications, name)
	return nil
}

// LookupPublication returns a copy of the named publication, or
// nil/false when it doesn't exist.
func (p *PubSub) LookupPublication(name string) (*Publication, bool) {
	p.mu.RLock()
	defer p.mu.RUnlock()
	pub, ok := p.publications[name]
	if !ok {
		return nil, false
	}
	out := *pub
	out.Tables = append([]string(nil), pub.Tables...)
	return &out, true
}

// CreatePublicationDuringRecovery is the idempotent version of
// CreatePublicationAsOwner used by the WAL-replay driver
// (internal/initdb/pubsub_ddl_recovery.go). Unlike CreatePublicationAsOwner
// it takes the OID from the WAL record (so the recovered publication
// matches the pre-crash OID exactly) and overwrites rather than erroring
// when a publication with the same name is already present (replay may see
// the same record more than once across a partial-then-full replay).
// Mirrors catalog.InMemory.CreateCollationDuringRecovery. DU-002
// restart-persistence follow-up (M0119-0004, loop #67 ledger resume point).
func (p *PubSub) CreatePublicationDuringRecovery(pub *Publication) {
	p.mu.Lock()
	defer p.mu.Unlock()
	out := *pub
	out.Tables = append([]string(nil), pub.Tables...)
	p.publications[pub.Name] = &out
	if pub.OID >= p.nextOID {
		p.nextOID = pub.OID + 1
	}
}

// DropPublicationDuringRecovery is the idempotent counterpart used for
// replaying RecordKindDropPublication. Identical to DropPublication but
// discards the found/not-found result — replay does not care whether the
// publication was still present. DU-002 restart-persistence follow-up
// (M0119-0004, loop #67 ledger resume point).
func (p *PubSub) DropPublicationDuringRecovery(name string) {
	_ = p.DropPublication(name)
}

// SetPublicationOwnerDuringRecovery is the discard-result recovery
// counterpart to SetPublicationOwner, mirroring
// DropPublicationDuringRecovery. DU-002 restart-persistence follow-up
// (M0119-0004, loop #67 ledger resume point).
func (p *PubSub) SetPublicationOwnerDuringRecovery(name string, owner uint32) {
	_ = p.SetPublicationOwner(name, owner)
}

// Publications returns every publication in name order. Each
// entry is a deep copy.
func (p *PubSub) Publications() []*Publication {
	p.mu.RLock()
	defer p.mu.RUnlock()
	names := make([]string, 0, len(p.publications))
	for n := range p.publications {
		names = append(names, n)
	}
	sort.Strings(names)
	out := make([]*Publication, 0, len(names))
	for _, n := range names {
		pub := *p.publications[n]
		pub.Tables = append([]string(nil), p.publications[n].Tables...)
		out = append(out, &pub)
	}
	return out
}

// CreateSubscription registers a new subscription owned by the bootstrap
// superuser. slotName defaults to name when empty (matches upstream).
// Callers that know the connection's currently-effective role should use
// CreateSubscriptionAsOwner instead.
func (p *PubSub) CreateSubscription(name, conninfo string, publications []string, slotName string, enabled bool) (*Subscription, error) {
	return p.CreateSubscriptionAsOwner(name, conninfo, publications, slotName, enabled, 10)
}

// CreateSubscriptionAsOwner is CreateSubscription with an explicit owner
// OID — the role that issued CREATE SUBSCRIPTION, mirroring
// Publication.Owner's same real-ownership convention. DU-002 slice 424.
func (p *PubSub) CreateSubscriptionAsOwner(name, conninfo string, publications []string, slotName string, enabled bool, owner uint32) (*Subscription, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if _, ok := p.subscriptions[name]; ok {
		return nil, ErrSubscriptionExists
	}
	if slotName == "" {
		slotName = name
	}
	sub := &Subscription{
		Name:         name,
		OID:          p.nextOID,
		Conninfo:     conninfo,
		Owner:        owner,
		Publications: append([]string(nil), publications...),
		Enabled:      enabled,
		SlotName:     slotName,
	}
	p.nextOID++
	p.subscriptions[name] = sub
	out := *sub
	out.Publications = append([]string(nil), sub.Publications...)
	return &out, nil
}

// SetSubscriptionOwner updates a subscription's owner OID. Returns
// ErrSubscriptionNotFound when name is unknown. Backs ALTER SUBSCRIPTION name
// OWNER TO newowner. DU-002 slice 425 (M0119-0004, loop #65 ledger
// follow-up).
func (p *PubSub) SetSubscriptionOwner(name string, owner uint32) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	sub, ok := p.subscriptions[name]
	if !ok {
		return ErrSubscriptionNotFound
	}
	sub.Owner = owner
	return nil
}

// DropSubscription removes a subscription. Returns
// ErrSubscriptionNotFound when name is unknown. Tablesync rows
// for the subscription are dropped at the same time so a
// re-CREATE under the same name doesn't inherit stale state.
func (p *PubSub) DropSubscription(name string) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if _, ok := p.subscriptions[name]; !ok {
		return ErrSubscriptionNotFound
	}
	delete(p.subscriptions, name)
	delete(p.subRels, name)
	return nil
}

// LookupSubscription returns a copy of the named subscription, or
// nil/false when it doesn't exist.
func (p *PubSub) LookupSubscription(name string) (*Subscription, bool) {
	p.mu.RLock()
	defer p.mu.RUnlock()
	sub, ok := p.subscriptions[name]
	if !ok {
		return nil, false
	}
	out := *sub
	out.Publications = append([]string(nil), sub.Publications...)
	return &out, true
}

// CreateSubscriptionDuringRecovery is the idempotent version of
// CreateSubscriptionAsOwner used by the WAL-replay driver
// (internal/initdb/pubsub_ddl_recovery.go). Mirrors
// CreatePublicationDuringRecovery. DU-002 restart-persistence follow-up
// (M0119-0004, loop #67 ledger resume point).
func (p *PubSub) CreateSubscriptionDuringRecovery(sub *Subscription) {
	p.mu.Lock()
	defer p.mu.Unlock()
	out := *sub
	out.Publications = append([]string(nil), sub.Publications...)
	p.subscriptions[sub.Name] = &out
	if sub.OID >= p.nextOID {
		p.nextOID = sub.OID + 1
	}
}

// DropSubscriptionDuringRecovery is the idempotent counterpart used for
// replaying RecordKindDropSubscription. Mirrors
// DropPublicationDuringRecovery — also clears any tablesync rows for the
// name, matching DropSubscription. DU-002 restart-persistence follow-up
// (M0119-0004, loop #67 ledger resume point).
func (p *PubSub) DropSubscriptionDuringRecovery(name string) {
	_ = p.DropSubscription(name)
}

// SetSubscriptionOwnerDuringRecovery is the discard-result recovery
// counterpart to SetSubscriptionOwner, mirroring
// SetPublicationOwnerDuringRecovery. DU-002 restart-persistence follow-up
// (M0119-0004, loop #67 ledger resume point).
func (p *PubSub) SetSubscriptionOwnerDuringRecovery(name string, owner uint32) {
	_ = p.SetSubscriptionOwner(name, owner)
}

// Subscriptions returns every subscription in name order. Each
// entry is a deep copy.
func (p *PubSub) Subscriptions() []*Subscription {
	p.mu.RLock()
	defer p.mu.RUnlock()
	names := make([]string, 0, len(p.subscriptions))
	for n := range p.subscriptions {
		names = append(names, n)
	}
	sort.Strings(names)
	out := make([]*Subscription, 0, len(names))
	for _, n := range names {
		sub := *p.subscriptions[n]
		sub.Publications = append([]string(nil), p.subscriptions[n].Publications...)
		out = append(out, &sub)
	}
	return out
}

// validSubRelStates is the set of legal state letters for
// SubscriptionRel.State. Anything outside this set surfaces as
// ErrInvalidSubRelState.
var validSubRelStates = map[string]struct{}{
	SubRelStateInit:     {},
	SubRelStateDataCopy: {},
	SubRelStateSyncDone: {},
	SubRelStateReady:    {},
}

// validSubRelTransitions encodes the upstream-shape state
// machine: i → d → s → r. Each step is monotonic; "going
// backwards" is rejected at the API boundary so the apply
// worker can't accidentally rewind a synced table.
//
//	i → d   (initial → data-copy starting)
//	d → s   (data copy finished, catching up to streaming)
//	s → r   (caught up; ready to join streaming apply)
//	any state → same state is allowed as a no-op LSN bump.
//
// Tablesync sometimes also lets `i → s` happen when the table
// was already empty at slot start — upstream optimises away
// the data-copy phase. v0 honours that shortcut too.
var validSubRelTransitions = map[string]map[string]struct{}{
	SubRelStateInit: {
		SubRelStateInit:     {},
		SubRelStateDataCopy: {},
		SubRelStateSyncDone: {},
	},
	SubRelStateDataCopy: {
		SubRelStateDataCopy: {},
		SubRelStateSyncDone: {},
	},
	SubRelStateSyncDone: {
		SubRelStateSyncDone: {},
		SubRelStateReady:    {},
	},
	SubRelStateReady: {
		SubRelStateReady: {},
	},
}

// AddSubscriptionRel registers a new tablesync row for
// (subName, relOID) in the SubRelStateInit state. Returns
// ErrSubscriptionNotFound when the subscription doesn't exist
// and ErrSubscriptionRelExists when the rel is already tracked.
// See docs/design/0008-0004-apply-worker-and-tablesync.md.
func (p *PubSub) AddSubscriptionRel(subName string, relOID uint32) (*SubscriptionRel, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	sub, ok := p.subscriptions[subName]
	if !ok {
		return nil, ErrSubscriptionNotFound
	}
	if rels, ok := p.subRels[subName]; ok {
		if _, dup := rels[relOID]; dup {
			return nil, ErrSubscriptionRelExists
		}
	} else {
		p.subRels[subName] = map[uint32]*SubscriptionRel{}
	}
	rel := &SubscriptionRel{
		SubID:  sub.OID,
		RelOID: relOID,
		State:  SubRelStateInit,
	}
	p.subRels[subName][relOID] = rel
	out := *rel
	return &out, nil
}

// AdvanceSubscriptionRel transitions one tablesync row to a new
// state, optionally bumping its LSN. The state must be one of
// `i`/`d`/`s`/`r`, and the transition must be legal under
// validSubRelTransitions. Mirrors upstream's
// `UpdateSubscriptionRelState`. Returns the freshly-updated row
// (deep-copied) so the caller can observe both new fields without
// re-Lookup.
func (p *PubSub) AdvanceSubscriptionRel(subName string, relOID uint32, newState string, newLSN uint64) (*SubscriptionRel, error) {
	if _, ok := validSubRelStates[newState]; !ok {
		return nil, ErrInvalidSubRelState
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	rels, ok := p.subRels[subName]
	if !ok {
		return nil, ErrSubscriptionRelNotFound
	}
	rel, ok := rels[relOID]
	if !ok {
		return nil, ErrSubscriptionRelNotFound
	}
	allowed, ok := validSubRelTransitions[rel.State]
	if !ok {
		return nil, ErrInvalidSubRelStateTransition
	}
	if _, ok := allowed[newState]; !ok {
		return nil, fmt.Errorf("%w: %s → %s", ErrInvalidSubRelStateTransition, rel.State, newState)
	}
	rel.State = newState
	if newLSN > rel.LSN {
		rel.LSN = newLSN
	}
	out := *rel
	return &out, nil
}

// LookupSubscriptionRel returns a deep copy of one tablesync
// row, or nil/false when the (subName, relOID) pair isn't
// tracked.
func (p *PubSub) LookupSubscriptionRel(subName string, relOID uint32) (*SubscriptionRel, bool) {
	p.mu.RLock()
	defer p.mu.RUnlock()
	rels, ok := p.subRels[subName]
	if !ok {
		return nil, false
	}
	rel, ok := rels[relOID]
	if !ok {
		return nil, false
	}
	out := *rel
	return &out, true
}

// SubscriptionRels returns every tablesync row for `subName` in
// RelOID order. Each entry is a deep copy. Empty slice when the
// subscription has no tablesync rows yet (or when it doesn't
// exist).
func (p *PubSub) SubscriptionRels(subName string) []*SubscriptionRel {
	p.mu.RLock()
	defer p.mu.RUnlock()
	rels, ok := p.subRels[subName]
	if !ok {
		return nil
	}
	oids := make([]uint32, 0, len(rels))
	for o := range rels {
		oids = append(oids, o)
	}
	sort.Slice(oids, func(i, j int) bool { return oids[i] < oids[j] })
	out := make([]*SubscriptionRel, 0, len(oids))
	for _, o := range oids {
		cp := *rels[o]
		out = append(out, &cp)
	}
	return out
}

// AllSubscriptionRels returns every (subName, rel) pair across
// every subscription, ordered by (subName, RelOID). Used by the
// pg_subscription_rel virtual view.
func (p *PubSub) AllSubscriptionRels() []*SubscriptionRel {
	p.mu.RLock()
	defer p.mu.RUnlock()
	subNames := make([]string, 0, len(p.subRels))
	for n := range p.subRels {
		subNames = append(subNames, n)
	}
	sort.Strings(subNames)
	var out []*SubscriptionRel
	for _, n := range subNames {
		rels := p.subRels[n]
		oids := make([]uint32, 0, len(rels))
		for o := range rels {
			oids = append(oids, o)
		}
		sort.Slice(oids, func(i, j int) bool { return oids[i] < oids[j] })
		for _, o := range oids {
			cp := *rels[o]
			out = append(out, &cp)
		}
	}
	return out
}
