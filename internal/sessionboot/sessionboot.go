// Package sessionboot derives the bounded operator-session packet from the
// canonical CD-0016 continuity projection. It owns no workflow derivation.
package sessionboot

import (
	"encoding/json"
	"fmt"
	"slices"

	"github.com/sharper-flow/concord/internal/agent"
	"github.com/sharper-flow/concord/internal/agentcontract"
	"github.com/sharper-flow/concord/internal/payloadschema"
	"github.com/sharper-flow/concord/internal/store"
)

const (
	SchemaVersion          = "1.0"
	SessionType            = "operator"
	SessionContractVersion = "1.0"
)

// MaxBootPacketBytes is the session boot packet's own cap. The boot packet is
// its own bounded population, not a request or a result envelope: the cap
// keeps the size the boot packet has always carried, named here so the boot
// population never moves when the request or response budgets change.
const MaxBootPacketBytes = 65536

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
	// the work identity to derive a boundary cursor. The packet carries the
	// inline projection whenever its measured complete-prefix fit fits the
	// boot packet's own cap; no smaller fixed payload cap sits below it.
	raw, size, err := agent.FitContinuity(snapshot, store.ContinuityRequest{Work: snapshot.WorkID}, MaxBootPacketBytes, func(fitted store.ContinuitySnapshot) ([]byte, int, error) {
		raw, err := json.Marshal(bootPacket(productID, fitted.WorkID, agent.ContinuityPayload(fitted)))
		return raw, len(raw), err
	})
	if err != nil {
		return nil, err
	}
	if size > MaxBootPacketBytes {
		// CD-0031: a fixed projection no complete-prefix reduction can
		// shrink travels as the closed deterministic projection reference of
		// the original captured snapshot, never of a reduced candidate. The
		// reference names the exact work, the canonical digest and length,
		// and the canonical initial continuity read; it mints no timestamp
		// and no signature.
		reference, err := agent.ContinuityProjectionReference(snapshot)
		if err != nil {
			return nil, err
		}
		raw, err = json.Marshal(bootPacket(productID, snapshot.WorkID, reference))
		if err != nil {
			return nil, err
		}
	}
	if err := Validate(raw); err != nil {
		return nil, err
	}
	return raw, nil
}

func bootPacket(productID, workID string, continuity map[string]any) Packet {
	return Packet{
		SchemaVersion: SchemaVersion, SessionType: SessionType,
		SessionContractVersion: SessionContractVersion,
		ManifestDigest:         agentcontract.ManifestDigest,
		ProductID:              productID, WorkID: workID,
		Continuity: continuity,
	}
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
	return nil
}
