package agent

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// AJ5 — scope a change by its footprint (CD-0222 D2, C22.DomainFootprint).
//
// bindAJ5FootprintOfAChange asks which current law governs a change to one
// law document and one code file. It proves each law arrives once under its
// strongest basis (the document outranks its behavior match), at the
// revision the index holds; that a code file the absent package map cannot
// place is flagged rather than dropped; and that the read neither inlines a
// law body nor appends to the authoritative log.
func bindAJ5FootprintOfAChange(t *testing.T, sc jobScenario) jobObservation {
	t.Helper()
	s, service, grant, home, _ := agentJobsKnowledgeFixture(t, false)
	ambient, _ := sc.InitialState["ambient_project"].(string)
	const document = ".concord/docs/decisions/CD-0002-state-authority.md"
	eventsBefore := domainEventCountForTest(t, s)

	resp := dispatchRead(t, s, service, InvokeRequest{
		Tool:      "concord_domain",
		Operation: "footprint",
		Input:     json.RawMessage(`{"product_id":"prod-alpha","paths":["` + document + `","internal/store/store.go"],"behavior":"sqlite","page":{"cursor":null,"limit":20}}`),
	}, agentJobsEnvelope(grant, ambient, "prod-alpha"))
	if resp.Outcome != OutcomeOK {
		t.Fatalf("footprint outcome=%s error=%+v", resp.Outcome, resp.Error)
	}
	var page struct {
		PackageMapContentHash *string `json:"package_map_content_hash"`
		Paths                 []struct {
			Path     string `json:"path"`
			Unmapped bool   `json:"unmapped"`
		} `json:"paths"`
		Laws []struct {
			LawID       string `json:"law_id"`
			Basis       string `json:"basis"`
			ContentHash string `json:"content_hash"`
		} `json:"laws"`
	}
	if err := json.Unmarshal(resp.Result, &page); err != nil {
		t.Fatalf("unmarshal footprint page: %v", err)
	}

	obs := envelopeToObservation(resp)
	bases := []any{}
	current := len(page.Laws) > 0
	for _, law := range page.Laws {
		bases = append(bases, law.LawID+":"+law.Basis)
		var indexed string
		if err := s.DatabaseForTesting().QueryRow(`SELECT content_hash FROM law_subjects WHERE law_id=?`, law.LawID).Scan(&indexed); err != nil {
			t.Fatalf("read indexed revision of %s: %v", law.LawID, err)
		}
		current = current && law.ContentHash == indexed
	}
	unmapped := []any{}
	for _, entry := range page.Paths {
		if entry.Unmapped {
			unmapped = append(unmapped, entry.Path)
		}
	}
	obs.Result = map[string]any{
		"law_bases":             bases,
		"law_revisions_current": current,
		"unmapped_paths":        unmapped,
	}
	if page.PackageMapContentHash != nil {
		obs.Result["package_map_content_hash"] = *page.PackageMapContentHash
	} else {
		obs.Result["package_map_content_hash"] = probedAbsent{}
	}

	obs.Effects = map[string]any{"law_body_inlined": probedAbsent{}, "footprint_mutation": probedAbsent{}}
	body, err := os.ReadFile(filepath.Join(home.RepoPath, filepath.FromSlash(document)))
	if err != nil {
		t.Fatalf("read committed law body: %v", err)
	}
	for _, line := range strings.Split(string(body), "\n") {
		if line = strings.TrimSpace(line); len(line) >= 24 && strings.Contains(string(resp.Result), line) {
			obs.Effects["law_body_inlined"] = line
		}
	}
	if after := domainEventCountForTest(t, s); after != eventsBefore {
		obs.Effects["footprint_mutation"] = after - eventsBefore
	}
	return obs
}
