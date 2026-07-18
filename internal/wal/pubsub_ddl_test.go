package wal

import (
	"reflect"
	"testing"
)

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

// TestDecodeSubscriptionRejectsWrongKindAndTruncatedPayload pins the
// subscription decoders' guards (publication kinds 50-52 retired to heap
// journaling in B3.3; only subscription records remain).
func TestDecodeSubscriptionRejectsWrongKindAndTruncatedPayload(t *testing.T) {
	bogus := EncodeDropSubscription("x")
	if _, _, _, _, _, _, _, _, err := DecodeCreateSubscription(bogus); err == nil {
		t.Error("DecodeCreateSubscription: expected error on wrong kind")
	}
	if _, _, err := DecodeAlterSubscriptionOwner(bogus); err == nil {
		t.Error("DecodeAlterSubscriptionOwner: expected error on wrong kind")
	}
	if _, err := DecodeDropSubscription([]byte{RecordKindDropSubscription, 10, 0}); err == nil {
		t.Error("DecodeDropSubscription: expected error on truncated payload")
	}
}
