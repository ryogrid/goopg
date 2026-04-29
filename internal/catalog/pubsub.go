package catalog

import (
	"errors"
	"sort"
	"sync"
)

// Publication is one CREATE PUBLICATION's catalog row. Mirrors
// the upstream pg_publication shape closely enough that operators
// who already know `\dRp` can read the goopg view without
// surprises. See
// docs/design/0008-0003-publication-subscription-ddl.md.
type Publication struct {
	Name          string
	OID           uint32
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
	Name         string
	OID          uint32
	Conninfo     string
	Publications []string
	Enabled      bool
	SlotName     string
}

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

// CreatePublication registers a new publication. tables is the
// qualified-table-name list (`"schema.name"`); pass nil for
// FOR ALL TABLES (with opts.AllTables = true) or for an empty
// FOR TABLE list. Returns ErrPublicationExists when name is
// taken.
func (p *PubSub) CreatePublication(name string, tables []string, opts PublicationOptions) (*Publication, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if _, ok := p.publications[name]; ok {
		return nil, ErrPublicationExists
	}
	pub := &Publication{
		Name:          name,
		OID:           p.nextOID,
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

// CreateSubscription registers a new subscription. slotName
// defaults to name when empty (matches upstream).
func (p *PubSub) CreateSubscription(name, conninfo string, publications []string, slotName string, enabled bool) (*Subscription, error) {
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

// DropSubscription removes a subscription. Returns
// ErrSubscriptionNotFound when name is unknown.
func (p *PubSub) DropSubscription(name string) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if _, ok := p.subscriptions[name]; !ok {
		return ErrSubscriptionNotFound
	}
	delete(p.subscriptions, name)
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
