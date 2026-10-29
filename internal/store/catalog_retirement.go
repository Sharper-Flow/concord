package store

// The managed-resource inventory and Domain-resource attachment surface are
// retired with no replacement catalogue. Their four event kinds keep only a
// refusal: the store
// folds no new catalog rows, and an append attempt fails with the retirement
// named rather than the generic unknown-kind failure.
const (
	retiredManagedResourceCreated       = "managed_resource.created"
	retiredManagedResourceConsumerAdded = "managed_resource.consumer_added"
	retiredManagedResourceUpdated       = "managed_resource.updated"
	retiredDomainResourceAttachments    = "domain.resource_attachments_replaced"
)

func catalogRetiredEventAppendRefused(kind string) error {
	return newFailure(KindInvalidOperation, "apply_operation",
		"event kind "+kind+" is retired by CON-1487: the managed-resource inventory and Domain-resource attachments have no replacement catalogue", false,
		"keep data/state facts with their bounded fact-local source identifier strings; no registration exists")
}
