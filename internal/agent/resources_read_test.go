package agent

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/sharper-flow/concord/internal/store"
	"github.com/sharper-flow/concord/internal/store/storetest"
)

// C15 §6 names one closed read operation on concord_product_view as the
// agent surface for the managed-resource inventory, and CD-0106 D4 makes
// that read the identity half of external-system knowledge. The runtime
// serves it from the selected Product and the result passes the closed
// resource_page schema.
func TestProductViewResourcesReadsTheSelectedProductInventory(t *testing.T) {
	s, err := storetest.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	ctx := context.Background()
	if _, err := s.DatabaseForTesting().Exec(`INSERT INTO fold_guard(active) VALUES (1);
		INSERT INTO projects(id,display_name,version,created_at,updated_at) VALUES ('owner-project','owner-project',1,'t','t');
		INSERT INTO products(id,display_name,stage_maturity,stage_audience_commitment,version,created_at,updated_at) VALUES ('owner-product','owner-product','prototype','operator_only',1,'t','t');
		INSERT INTO product_projects(product_id,project_id,role) VALUES ('owner-product','owner-project','primary');
		DELETE FROM fold_guard`); err != nil {
		t.Fatal(err)
	}
	if _, err := store.CreateManagedResource(ctx, s, store.ManagedResourceCreateRequest{
		EventID: "res-created", ResourceID: "vendor-api", ProductID: "owner-product",
		DisplayName: "Vendor API", Class: "saas", Kind: "service", Purpose: "hosted pricing feed",
		StageMaturity: "production", StageAudienceCommitment: "limited", Environments: []string{"production"},
		MetadataSchemaVersion: "1", Metadata: []byte(`{"documentation_locator":"/vendor/api-docs"}`),
		ExpectedProductVersion: 1, Actor: "operator", OccurredAt: time.Date(2026, 9, 3, 12, 0, 0, 0, time.UTC),
	}); err != nil {
		t.Fatal(err)
	}
	r := runtime{Store: s, Tool: "concord_product_view", Operation: "resources", Envelope: CallEnvelope{SelectedProductID: "owner-product"}}
	response, err := r.read(ctx, NewBase("runtime-c15", r.Tool, r.Operation), json.RawMessage(`{"page":{"cursor":null,"limit":10}}`), "C15.Resources")
	if err != nil {
		t.Fatal(err)
	}
	if response.Outcome != OutcomeOK {
		t.Fatalf("resources response=%+v error=%#v", response, response.Error)
	}
	if err := ValidateOperationPayload("concord_product_view", "resources", response.Result, true); err != nil {
		t.Fatalf("resource_page failed closed-schema validation: %v", err)
	}
	var result struct {
		Resources []struct {
			ResourceID   string `json:"resource_id"`
			MetadataJSON string `json:"metadata_json"`
			Owner        struct {
				ProductID string `json:"product_id"`
			} `json:"owner"`
		} `json:"resources"`
	}
	if err := json.Unmarshal(response.Result, &result); err != nil {
		t.Fatal(err)
	}
	if len(result.Resources) != 1 || result.Resources[0].ResourceID != "vendor-api" || result.Resources[0].Owner.ProductID != "owner-product" {
		t.Fatalf("resources = %+v", result.Resources)
	}
	if result.Resources[0].MetadataJSON != `{"documentation_locator":"/vendor/api-docs"}` {
		t.Fatalf("metadata_json = %q", result.Resources[0].MetadataJSON)
	}
}

func TestProductViewResourcesInputIsClosed(t *testing.T) {
	if err := ValidateOperationPayload("concord_product_view", "resources", []byte(`{"resource_id":"vendor-api","class":"saas"}`), false); err != nil {
		t.Fatalf("valid resources input refused: %v", err)
	}
	if err := ValidateOperationPayload("concord_product_view", "resources", []byte(`{"product_id":"p","class":"cloud"}`), false); err == nil {
		t.Fatal("unknown class passed the closed input")
	}
	if err := ValidateOperationPayload("concord_product_view", "resources", []byte(`{"product_id":"p","write":true}`), false); err == nil {
		t.Fatal("unknown field passed the closed input")
	}
}
