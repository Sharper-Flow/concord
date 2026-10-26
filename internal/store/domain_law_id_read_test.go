package store

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/sharper-flow/concord/internal/payloadschema"
)

func TestDomainDetailReadAdmitsManifestLawIDs(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct{ name, lawID string }{
		{"129_characters", strings.Repeat("x", 129)},
		{"256_unicode_characters", strings.Repeat("界", 256)},
		{"internal_spaces", "law with spaces"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			s := seedDomainReadStoreWithLawID(t, tc.lawID)
			detail, err := s.QueryDomainDetail(context.Background(), DomainDetailRequest{Product: "concord", Domain: "sync"})
			if err != nil {
				t.Fatal(err)
			}
			if len(detail.CurrentLaw) != 1 || detail.CurrentLaw[0].LawID != tc.lawID {
				t.Fatalf("current law did not preserve manifest ID: %#v", detail.CurrentLaw)
			}
			if len(detail.Relations) != 1 || len(detail.Relations[0].GoverningLaws) != 1 || detail.Relations[0].GoverningLaws[0] != tc.lawID {
				t.Fatalf("relation did not preserve manifest law ID: %#v", detail.Relations)
			}
			raw, err := json.Marshal(NewDomainDetailPayload(detail))
			if err != nil {
				t.Fatal(err)
			}
			var value any
			if err := json.Unmarshal(raw, &value); err != nil {
				t.Fatal(err)
			}
			document := payloadschema.Document()
			definition := document["$defs"].(map[string]any)["domain_detail_result"].(map[string]any)
			if err := payloadschema.ValidateValue(value, definition, document, "$"); err != nil {
				t.Fatalf("domain detail schema rejected manifest-valid law ID: %v", err)
			}
		})
	}
}
