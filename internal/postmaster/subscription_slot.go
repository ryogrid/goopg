package postmaster

import (
	"errors"
	"fmt"

	"github.com/goopg/goopg/internal/executor"
	"github.com/goopg/goopg/internal/replication"
)

// subscriptionSlotHooks returns the executor hooks that create and drop a
// subscription's slot on its publisher, mapping failures onto
// subscriptioncmds.c's errors:
//
//   - the publisher cannot be reached: 08006 "subscription \"%s\" could not
//     connect to the publisher: %s" (CREATE), or 08006 "could not connect to
//     publisher when attempting to drop replication slot \"%s\": %s" with
//     its ALTER SUBSCRIPTION hint (DROP);
//   - the publisher refuses CREATE_REPLICATION_SLOT: 08P01 "could not create
//     replication slot \"%s\": %s" (libpqrcv_create_slot);
//   - the publisher refuses DROP_REPLICATION_SLOT: 08006 "could not drop
//     replication slot \"%s\" on publisher: %s"
//     (ReplicationSlotDropAtPubNode with missing_ok = false).
func (s *Server) subscriptionSlotHooks() (create, drop func(subName, conninfo, slotName string) error) {
	create = func(subName, conninfo, slotName string) error {
		err := s.applyLauncher.CreatePublisherSlot(conninfo, slotName)
		var rce *replication.RemoteCommandError
		switch {
		case err == nil:
			return nil
		case errors.As(err, &rce):
			return &executor.ExecError{Code: "08P01",
				Message: fmt.Sprintf("could not create replication slot \"%s\": %s", slotName, rce.Error())}
		default:
			return &executor.ExecError{Code: "08006",
				Message: fmt.Sprintf("subscription \"%s\" could not connect to the publisher: %v", subName, errors.Unwrap(err))}
		}
	}
	drop = func(subName, conninfo, slotName string) error {
		// DropSubscription stops the subscription's workers before it
		// drops the slot; the apply worker would keep it active.
		s.applyLauncher.StopWorker(subName)
		err := s.applyLauncher.DropPublisherSlot(conninfo, slotName)
		var rce *replication.RemoteCommandError
		switch {
		case err == nil:
			return nil
		case errors.As(err, &rce):
			return &executor.ExecError{Code: "08006",
				Message: fmt.Sprintf("could not drop replication slot \"%s\" on publisher: %s", slotName, rce.Error())}
		default:
			return &executor.ExecError{Code: "08006",
				Message: fmt.Sprintf("could not connect to publisher when attempting to drop replication slot \"%s\": %v", slotName, errors.Unwrap(err)),
				Hint:    "Use ALTER SUBSCRIPTION ... DISABLE to disable the subscription, and then use ALTER SUBSCRIPTION ... SET (slot_name = NONE) to disassociate it from the slot."}
		}
	}
	return create, drop
}
