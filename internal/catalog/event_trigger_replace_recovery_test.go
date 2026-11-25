package catalog

import "testing"

// TestReplaceEventTriggersDuringRecovery pins the running standby's event
// trigger refresh (M0149-0077h): the replayed pg_event_trigger heap is the
// whole truth, so a replace drops triggers the primary dropped or renamed and
// picks up new ones and their ALTERed enabled state, with the TID cache
// following.
func TestReplaceEventTriggersDuringRecovery(t *testing.T) {
	c := NewInMemory()
	c.RegisterEventTriggerDuringRecovery(&EventTrigger{OID: 20001, Name: "old", Event: "login", Enabled: "O"})
	c.SetEventTriggerHeapTID(20001, SchemaHeapTID{Block: 0, Offset: 1})

	c.ReplaceEventTriggersDuringRecovery([]EventTrigger{
		{OID: 20001, Name: "renamed", Event: "login", Enabled: "A"},
		{OID: 20002, Name: "fresh", Event: "ddl_command_end", Enabled: "O", Tags: []string{"CREATE TABLE"}},
	}, map[uint32]SchemaHeapTID{20001: {Block: 0, Offset: 3}, 20002: {Block: 0, Offset: 4}})

	if _, ok := c.LookupEventTrigger("old"); ok {
		t.Fatal("renamed-away trigger still registered under its old name")
	}
	et, ok := c.LookupEventTrigger("renamed")
	if !ok || et.Enabled != "A" {
		t.Fatalf("renamed = %+v, %v; want enabled A", et, ok)
	}
	if got := c.ListEventTriggers(); len(got) != 2 || got[0].OID != 20001 || got[1].Tags[0] != "CREATE TABLE" {
		t.Fatalf("ListEventTriggers = %+v", got)
	}
	if tid, ok := c.EventTriggerHeapTID(20001); !ok || tid.Offset != 3 {
		t.Fatalf("TID(20001) = %+v, %v; want offset 3", tid, ok)
	}

	c.ReplaceEventTriggersDuringRecovery(nil, nil)
	if got := c.ListEventTriggers(); len(got) != 0 {
		t.Fatalf("after dropping all: %+v", got)
	}
	if _, ok := c.EventTriggerHeapTID(20002); ok {
		t.Fatal("dropped trigger's TID survived")
	}
}
