package store

const (
	retiredManagedResourceCreated       = "managed_resource.created"
	retiredManagedResourceConsumerAdded = "managed_resource.consumer_added"
	retiredManagedResourceUpdated       = "managed_resource.updated"
	retiredDomainResourceAttachments    = "domain.resource_attachments_replaced"
)

func catalogRetiredEventAppendRefused(kind string) error {
	return newFailure(KindInvalidOperation, "apply_operation",
		"event kind "+kind+" is unavailable: managed-resource inventory and Domain-resource attachments are not supported", false,
		"keep data/state facts with their bounded fact-local source identifier strings; no registration exists")
}
