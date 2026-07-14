package wal

import (
	"reflect"
	"testing"
)

// TestEncodeDecodeCreatePublicationRoundTrip pins the DU-002
// restart-persistence (M0119-0004, loop #67 ledger resume point) CREATE
// PUBLICATION WAL record format. Encode → Decode must return the original
// name/tables/OIDs/flags.
func TestEncodeDecodeCreatePublicationRoundTrip(t *testing.T) {
	cases := []struct {
		name                                                   string
		tables                                                 []string
		oid, ownerOID                                          uint32
		allTables, publishInsert, publishUpdate, publishDelete bool
	}{
		{"mypub", nil, 16384, 10, true, true, true, true},
		{"tblpub", []string{"public.t1", "public.t2"}, 16385, 16400, false, true, false, true},
		{"empty", []string{}, 16386, 10, false, false, false, false},
		{"日本語パブ", []string{"myschema.日本語表"}, 4294967295, 16400, false, true, true, false}, // multi-byte UTF-8, max OID
	}
	for _, c := range cases {
		raw := EncodeCreatePublication(c.name, c.tables, c.oid, c.ownerOID, c.allTables, c.publishInsert, c.publishUpdate, c.publishDelete)
		if raw[0] != RecordKindCreatePublication {
			t.Errorf("%q: kind byte = %d, want %d", c.name, raw[0], RecordKindCreatePublication)
			continue
		}
		gotName, gotTables, gotOID, gotOwnerOID, gotAllTables, gotPublishInsert, gotPublishUpdate, gotPublishDelete, err := DecodeCreatePublication(raw)
		if err != nil {
			t.Errorf("%q: decode err: %v", c.name, err)
			continue
		}
		if gotName != c.name || gotOID != c.oid || gotOwnerOID != c.ownerOID ||
			gotAllTables != c.allTables || gotPublishInsert != c.publishInsert ||
			gotPublishUpdate != c.publishUpdate || gotPublishDelete != c.publishDelete {
			t.Errorf("%q: decoded (%q, %d, %d, %v, %v, %v, %v)",
				c.name, gotName, gotOID, gotOwnerOID, gotAllTables, gotPublishInsert, gotPublishUpdate, gotPublishDelete)
		}
		if len(c.tables) == 0 && len(gotTables) != 0 {
			t.Errorf("%q: tables = %v, want empty", c.name, gotTables)
		} else if len(c.tables) > 0 && !reflect.DeepEqual(gotTables, c.tables) {
			t.Errorf("%q: tables = %v, want %v", c.name, gotTables, c.tables)
		}
	}
}

// TestEncodeDecodeDropPublicationRoundTrip is the DROP counterpart.
func TestEncodeDecodeDropPublicationRoundTrip(t *testing.T) {
	for _, name := range []string{"mypub", "日本語パブ"} {
		raw := EncodeDropPublication(name)
		if raw[0] != RecordKindDropPublication {
			t.Errorf("%q: kind byte = %d, want %d", name, raw[0], RecordKindDropPublication)
			continue
		}
		got, err := DecodeDropPublication(raw)
		if err != nil {
			t.Errorf("%q: decode err: %v", name, err)
			continue
		}
		if got != name {
			t.Errorf("decoded %q, want %q", got, name)
		}
	}
}

// TestEncodeDecodeAlterPublicationOwnerRoundTrip is the OWNER TO counterpart.
func TestEncodeDecodeAlterPublicationOwnerRoundTrip(t *testing.T) {
	cases := []struct {
		name     string
		ownerOID uint32
	}{
		{"mypub", 16400},
		{"日本語パブ", 4294967295},
	}
	for _, c := range cases {
		raw := EncodeAlterPublicationOwner(c.name, c.ownerOID)
		if raw[0] != RecordKindAlterPublicationOwner {
			t.Errorf("%q: kind byte = %d, want %d", c.name, raw[0], RecordKindAlterPublicationOwner)
			continue
		}
		gotName, gotOwnerOID, err := DecodeAlterPublicationOwner(raw)
		if err != nil {
			t.Errorf("%q: decode err: %v", c.name, err)
			continue
		}
		if gotName != c.name || gotOwnerOID != c.ownerOID {
			t.Errorf("%q: decoded (%q, %d)", c.name, gotName, gotOwnerOID)
		}
	}
}

// TestEncodeDecodeCreateSubscriptionRoundTrip pins the CREATE SUBSCRIPTION
// WAL record format.
func TestEncodeDecodeCreateSubscriptionRoundTrip(t *testing.T) {
	cases := []struct {
		name, conninfo, slotName string
		publications             []string
		oid, ownerOID            uint32
		enabled                  bool
		dbOid                    uint32
	}{
		{"mysub", "host=localhost dbname=foo", "mysub", []string{"pub1"}, 16384, 10, true, 0},
		{"multisub", "host=other", "customslot", []string{"pub1", "pub2"}, 16385, 16400, false, 16401},
		{"日本語サブ", "host=localhost", "日本語スロット", []string{"日本語パブ"}, 4294967295, 16400, true, 4294967294}, // multi-byte UTF-8, max OID
	}
	for _, c := range cases {
		raw := EncodeCreateSubscription(c.name, c.conninfo, c.slotName, c.publications, c.oid, c.ownerOID, c.enabled, c.dbOid)
		if raw[0] != RecordKindCreateSubscription {
			t.Errorf("%q: kind byte = %d, want %d", c.name, raw[0], RecordKindCreateSubscription)
			continue
		}
		gotName, gotConninfo, gotSlotName, gotPubs, gotOID, gotOwnerOID, gotEnabled, gotDBOid, err := DecodeCreateSubscription(raw)
		if err != nil {
			t.Errorf("%q: decode err: %v", c.name, err)
			continue
		}
		if gotName != c.name || gotConninfo != c.conninfo || gotSlotName != c.slotName ||
			gotOID != c.oid || gotOwnerOID != c.ownerOID || gotEnabled != c.enabled || gotDBOid != c.dbOid {
			t.Errorf("%q: decoded (%q, %q, %q, %d, %d, %v, %d)", c.name, gotName, gotConninfo, gotSlotName, gotOID, gotOwnerOID, gotEnabled, gotDBOid)
		}
		if !reflect.DeepEqual(gotPubs, c.publications) {
			t.Errorf("%q: publications = %v, want %v", c.name, gotPubs, c.publications)
		}
	}
}

// TestEncodeDecodeDropSubscriptionRoundTrip is the DROP counterpart.
func TestEncodeDecodeDropSubscriptionRoundTrip(t *testing.T) {
	for _, name := range []string{"mysub", "日本語サブ"} {
		raw := EncodeDropSubscription(name)
		if raw[0] != RecordKindDropSubscription {
			t.Errorf("%q: kind byte = %d, want %d", name, raw[0], RecordKindDropSubscription)
			continue
		}
		got, err := DecodeDropSubscription(raw)
		if err != nil {
			t.Errorf("%q: decode err: %v", name, err)
			continue
		}
		if got != name {
			t.Errorf("decoded %q, want %q", got, name)
		}
	}
}

// TestEncodeDecodeAlterSubscriptionOwnerRoundTrip is the OWNER TO
// counterpart.
func TestEncodeDecodeAlterSubscriptionOwnerRoundTrip(t *testing.T) {
	cases := []struct {
		name     string
		ownerOID uint32
	}{
		{"mysub", 16400},
		{"日本語サブ", 4294967295},
	}
	for _, c := range cases {
		raw := EncodeAlterSubscriptionOwner(c.name, c.ownerOID)
		if raw[0] != RecordKindAlterSubscriptionOwner {
			t.Errorf("%q: kind byte = %d, want %d", c.name, raw[0], RecordKindAlterSubscriptionOwner)
			continue
		}
		gotName, gotOwnerOID, err := DecodeAlterSubscriptionOwner(raw)
		if err != nil {
			t.Errorf("%q: decode err: %v", c.name, err)
			continue
		}
		if gotName != c.name || gotOwnerOID != c.ownerOID {
			t.Errorf("%q: decoded (%q, %d)", c.name, gotName, gotOwnerOID)
		}
	}
}

// TestDecodePubSubRejectsWrongKindAndTruncatedPayload guards the decoders
// against a mismatched kind byte and a corrupt/truncated on-disk record, for
// every publication/subscription record kind.
func TestDecodePubSubRejectsWrongKindAndTruncatedPayload(t *testing.T) {
	bogus := EncodeDropPublication("x")

	if _, _, _, _, _, _, _, _, err := DecodeCreatePublication(bogus); err == nil {
		t.Error("DecodeCreatePublication: expected error on wrong kind")
	}
	if _, err := DecodeDropPublication(EncodeCreatePublication("x", nil, 1, 10, false, true, true, true)); err == nil {
		t.Error("DecodeDropPublication: expected error on wrong kind")
	}
	if _, _, err := DecodeAlterPublicationOwner(bogus); err == nil {
		t.Error("DecodeAlterPublicationOwner: expected error on wrong kind")
	}
	if _, _, _, _, _, _, _, _, err := DecodeCreateSubscription(bogus); err == nil {
		t.Error("DecodeCreateSubscription: expected error on wrong kind")
	}
	if _, err := DecodeDropSubscription(bogus); err == nil {
		t.Error("DecodeDropSubscription: expected error on wrong kind")
	}
	if _, _, err := DecodeAlterSubscriptionOwner(bogus); err == nil {
		t.Error("DecodeAlterSubscriptionOwner: expected error on wrong kind")
	}

	truncCases := []struct {
		name    string
		payload []byte
		decode  func([]byte) error
	}{
		{"CreatePublication", []byte{RecordKindCreatePublication, 0, 0, 0, 0}, func(p []byte) error {
			_, _, _, _, _, _, _, _, err := DecodeCreatePublication(p)
			return err
		}},
		{"DropPublication", []byte{RecordKindDropPublication, 10, 0}, func(p []byte) error {
			_, err := DecodeDropPublication(p)
			return err
		}},
		{"AlterPublicationOwner", []byte{RecordKindAlterPublicationOwner, 0, 0, 0, 0, 0}, func(p []byte) error {
			_, _, err := DecodeAlterPublicationOwner(p)
			return err
		}},
		{"CreateSubscription", []byte{RecordKindCreateSubscription, 0, 0, 0, 0}, func(p []byte) error {
			_, _, _, _, _, _, _, _, err := DecodeCreateSubscription(p)
			return err
		}},
		{"DropSubscription", []byte{RecordKindDropSubscription, 10, 0}, func(p []byte) error {
			_, err := DecodeDropSubscription(p)
			return err
		}},
		{"AlterSubscriptionOwner", []byte{RecordKindAlterSubscriptionOwner, 0, 0, 0, 0, 0}, func(p []byte) error {
			_, _, err := DecodeAlterSubscriptionOwner(p)
			return err
		}},
	}
	for _, tc := range truncCases {
		if err := tc.decode(tc.payload); err == nil {
			t.Errorf("%s: expected error decoding truncated payload", tc.name)
		}
	}
}
