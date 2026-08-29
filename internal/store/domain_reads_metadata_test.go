package store

import "testing"

// CD-0041 D4 gives the Domain-to-resource relation bounded purpose and
// environment metadata. The read projection dropped both until #387, so an
// operator could record a purpose no agent could ever read back.
func TestDomainAttachmentsPayloadKeepsResourceMetadata(t *testing.T) {
	result := DomainAttachmentsResult{
		Attachments: DomainAttachmentView{
			ResourceEdges: []DomainResourceAttachment{{
				ResourceID:   "queue",
				Purpose:      "dispatches synchronization work",
				Environments: []string{"production"},
			}},
		},
	}

	payload := NewDomainAttachmentsPayload(result)

	if len(payload.Attachments.ResourceEdges) != 1 {
		t.Fatalf("resource edges = %d, want 1", len(payload.Attachments.ResourceEdges))
	}
	edge := payload.Attachments.ResourceEdges[0]
	if edge.ResourceID != "queue" {
		t.Fatalf("resource_id = %q, want queue", edge.ResourceID)
	}
	if edge.Purpose != "dispatches synchronization work" {
		t.Fatalf("purpose = %q, want the recorded purpose", edge.Purpose)
	}
	if len(edge.Environments) != 1 || edge.Environments[0] != "production" {
		t.Fatalf("environments = %#v, want [production]", edge.Environments)
	}
}

// An edge recorded without environments must project an empty array rather
// than a null, because the result schema declares environments as required.
func TestDomainAttachmentsPayloadProjectsEmptyEnvironments(t *testing.T) {
	payload := NewDomainAttachmentsPayload(DomainAttachmentsResult{
		Attachments: DomainAttachmentView{
			ResourceEdges: []DomainResourceAttachment{{ResourceID: "queue", Purpose: "stores data"}},
		},
	})
	if got := payload.Attachments.ResourceEdges[0].Environments; got == nil || len(got) != 0 {
		t.Fatalf("environments = %#v, want an empty array", got)
	}
}
