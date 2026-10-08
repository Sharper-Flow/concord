// Package sessionboot derives the bounded operator-session packet from the
// canonical CD-0016 continuity projection. It owns no workflow derivation.
package sessionboot

import (
	"encoding/json"
	"fmt"
	"slices"

	"github.com/sharper-flow/concord/internal/agent"
	"github.com/sharper-flow/concord/internal/store"
)

const (
	SchemaVersion          = "1.0"
	SessionType            = "operator"
	SessionContractVersion = "1.0"
)

type Packet struct {
	SchemaVersion          string         `json:"schema_version"`
	SessionType            string         `json:"session_type"`
	SessionContractVersion string         `json:"session_contract_version"`
	ManifestDigest         string         `json:"manifest_digest"`
	ProductID              string         `json:"product_id"`
	WorkID                 string         `json:"work_id"`
	Continuity             map[string]any `json:"continuity"`
}

func Build(productID string, snapshot store.ContinuitySnapshot) ([]byte, error) {
	if !slices.Contains(snapshot.ProductIdentity, productID) {
		return nil, fmt.Errorf("session boot Product identity %q is not bound to work %q", productID, snapshot.WorkID)
	}
	// The boot read pages boundaries from the start, so the fit needs only
	// the work identity to derive a boundary cursor.
	raw, _, err := agent.FitContinuity(snapshot, store.ContinuityRequest{Work: snapshot.WorkID}, agent.MaxEnvelopeBytes, func(fitted store.ContinuitySnapshot) ([]byte, int, error) {
		raw, err := json.Marshal(Packet{
			SchemaVersion: SchemaVersion, SessionType: SessionType,
			SessionContractVersion: SessionContractVersion,
			ManifestDigest:         agent.ManifestDigest,
			ProductID:              productID, WorkID: fitted.WorkID,
			Continuity: agent.ContinuityPayload(fitted),
		})
		return raw, len(raw), err
	})
	if err != nil {
		return nil, err
	}
	if err := Validate(raw); err != nil {
		return nil, err
	}
	return raw, nil
}

func Validate(raw []byte) error {
	if len(raw) > agent.MaxEnvelopeBytes {
		return fmt.Errorf("session boot packet exceeds %d bytes", agent.MaxEnvelopeBytes)
	}
	if err := agent.ValidatePayloadSchema("session_boot_packet", raw); err != nil {
		return fmt.Errorf("session boot packet schema: %w", err)
	}
	var packet Packet
	if err := json.Unmarshal(raw, &packet); err != nil {
		return err
	}
	if packet.SchemaVersion != SchemaVersion || packet.SessionType != SessionType || packet.SessionContractVersion != SessionContractVersion {
		return fmt.Errorf("session boot packet identity is not supported")
	}
	if packet.ManifestDigest != agent.ManifestDigest {
		return fmt.Errorf("session boot manifest digest mismatch")
	}
	return nil
}
