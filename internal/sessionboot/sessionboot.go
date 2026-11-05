// Package sessionboot renders the stored resume plan and handoff frontier.
// It owns no workflow derivation or retained projection.
package sessionboot

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"slices"

	"github.com/sharper-flow/concord/internal/agentcontract"
	"github.com/sharper-flow/concord/internal/payloadschema"
	"github.com/sharper-flow/concord/internal/store"
)

const (
	SchemaVersion          = "1.0"
	SessionType            = "operator"
	SessionContractVersion = "2.0"
	MaxBootPacketBytes     = 65536
)

type Packet struct {
	SchemaVersion          string          `json:"schema_version"`
	SessionType            string          `json:"session_type"`
	SessionContractVersion string          `json:"session_contract_version"`
	ManifestDigest         string          `json:"manifest_digest"`
	ProductID              string          `json:"product_id"`
	ProjectID              string          `json:"project_id"`
	WorkID                 string          `json:"work_id"`
	SourceVersionWatermark int64           `json:"source_version_watermark"`
	WorkflowInstance       string          `json:"workflow_instance"`
	Resume                 json.RawMessage `json:"resume"`
	PendingProjectHandoff  json.RawMessage `json:"pending_project_handoff,omitempty"`
}

// Build preserves the captured documents inline whenever the complete packet
// fits. References require reconstruction through the existing current reads;
// source drift requires preparing another packet, never historical replay.
func Build(productID, projectID string, snapshot store.SessionResumeRead, fits func([]byte) (bool, error)) ([]byte, error) {
	if fits == nil {
		return nil, fmt.Errorf("session boot packet fit measurement is required")
	}
	packet, err := inlinePacket(productID, projectID, snapshot)
	if err != nil {
		return nil, err
	}
	raw, err := json.Marshal(packet)
	if err != nil {
		return nil, err
	}
	fit := false
	if len(raw) <= MaxBootPacketBytes {
		fit, err = fits(raw)
		if err != nil {
			return nil, err
		}
	}
	if !fit {
		if !snapshot.WorkflowInstanceAbsent {
			packet.Resume, err = reference(packet.Resume, map[string]any{
				"tool": "concord_work_trace", "operation": "resume",
				"input": map[string]any{"work_id": snapshot.WorkID},
			}, "")
			if err != nil {
				return nil, err
			}
		}
		if snapshot.PendingProjectHandoff != nil {
			packet.PendingProjectHandoff, err = reference(packet.PendingProjectHandoff, map[string]any{
				"tool": "concord_work_browse", "operation": "list",
				"input": map[string]any{"product_id": productID, "project_ids": []string{projectID}, "work_ids": []string{snapshot.WorkID}, "detail": "full"},
			}, "items[0].pending_project_handoff")
			if err != nil {
				return nil, err
			}
		}
		raw, err = json.Marshal(packet)
		if err != nil {
			return nil, err
		}
	}
	if err := Validate(raw); err != nil {
		return nil, err
	}
	fit, err = fits(raw)
	if err != nil {
		return nil, err
	}
	if !fit {
		return nil, fmt.Errorf("session boot packet does not fit caller output")
	}
	return raw, nil
}

func inlinePacket(productID, projectID string, snapshot store.SessionResumeRead) (Packet, error) {
	packet := Packet{
		SchemaVersion: SchemaVersion, SessionType: SessionType,
		SessionContractVersion: SessionContractVersion, ManifestDigest: agentcontract.ManifestDigest,
		ProductID: productID, ProjectID: projectID, WorkID: snapshot.WorkID,
		SourceVersionWatermark: snapshot.SourceVersionWatermark,
		WorkflowInstance:       store.WorkflowInstancePresent, Resume: json.RawMessage("null"),
	}
	if !slices.Contains(snapshot.ProductIdentity, productID) {
		return packet, fmt.Errorf("session boot Product identity %q is not bound to work %q", productID, snapshot.WorkID)
	}
	if snapshot.WorkflowInstanceAbsent {
		if snapshot.Resume != nil {
			return packet, fmt.Errorf("session boot absent workflow carries a resume document")
		}
		packet.WorkflowInstance = store.WorkflowInstanceAbsent
	} else {
		if snapshot.Resume == nil || snapshot.Resume.WorkID != snapshot.WorkID || snapshot.Resume.SourceVersionWatermark != snapshot.SourceVersionWatermark {
			return packet, fmt.Errorf("session boot resume identity or watermark mismatch")
		}
		raw, err := json.Marshal(snapshot.Resume)
		if err != nil {
			return packet, err
		}
		if err := payloadschema.Validate("resume_read_document", raw); err != nil {
			return packet, err
		}
		packet.Resume = raw
	}
	if snapshot.PendingProjectHandoff != nil {
		raw, err := json.Marshal(snapshot.PendingProjectHandoff)
		if err != nil {
			return packet, err
		}
		if err := payloadschema.Validate("pending_project_handoff", raw); err != nil {
			return packet, err
		}
		packet.PendingProjectHandoff = raw
	}
	return packet, nil
}

func reference(document []byte, pointer map[string]any, path string) (json.RawMessage, error) {
	value := map[string]any{
		"projection_omitted": true, "digest": fmt.Sprintf("sha256:%x", sha256.Sum256(document)),
		"total_bytes": len(document), "read_pointer": pointer,
	}
	if path != "" {
		value["projection_path"] = path
	}
	return json.Marshal(value)
}

func Validate(raw []byte) error {
	if len(raw) > MaxBootPacketBytes {
		return fmt.Errorf("session boot packet exceeds %d bytes", MaxBootPacketBytes)
	}
	if err := payloadschema.Validate("session_boot_packet", raw); err != nil {
		return fmt.Errorf("session boot packet schema: %w", err)
	}
	var packet Packet
	if err := json.Unmarshal(raw, &packet); err != nil {
		return err
	}
	if packet.SchemaVersion != SchemaVersion || packet.SessionType != SessionType || packet.SessionContractVersion != SessionContractVersion {
		return fmt.Errorf("session boot packet identity is not supported")
	}
	if packet.ManifestDigest != agentcontract.ManifestDigest {
		return fmt.Errorf("session boot manifest digest mismatch")
	}
	if packet.WorkflowInstance == store.WorkflowInstancePresent {
		if err := validateDocumentIdentity(packet.Resume, packet, false); err != nil {
			return err
		}
	}
	if len(packet.PendingProjectHandoff) != 0 {
		return validateDocumentIdentity(packet.PendingProjectHandoff, packet, true)
	}
	return nil
}

func validateDocumentIdentity(raw json.RawMessage, packet Packet, handoff bool) error {
	var document struct {
		WorkID            string `json:"work_id"`
		ProjectionOmitted bool   `json:"projection_omitted"`
		ReadPointer       struct {
			Input struct {
				WorkID     string   `json:"work_id"`
				ProductID  string   `json:"product_id"`
				WorkIDs    []string `json:"work_ids"`
				ProjectIDs []string `json:"project_ids"`
			} `json:"input"`
		} `json:"read_pointer"`
	}
	if err := json.Unmarshal(raw, &document); err != nil {
		return err
	}
	if !document.ProjectionOmitted {
		if document.WorkID != packet.WorkID {
			return fmt.Errorf("session boot document work identity mismatch")
		}
		return nil
	}
	input := document.ReadPointer.Input
	if handoff {
		if input.ProductID != packet.ProductID || !slices.Equal(input.WorkIDs, []string{packet.WorkID}) || !slices.Equal(input.ProjectIDs, []string{packet.ProjectID}) {
			return fmt.Errorf("session boot handoff read identity mismatch")
		}
	} else if input.WorkID != packet.WorkID {
		return fmt.Errorf("session boot resume read identity mismatch")
	}
	return nil
}
