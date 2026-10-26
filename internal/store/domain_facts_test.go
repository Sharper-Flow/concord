package store

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"reflect"
	"strings"
	"testing"
)

func domainFactFixture(kind DomainFactKind) (DomainFact, DomainFactReferences) {
	anchor := DomainFactRepositoryAnchor{ProjectID: "project-a", Path: "src/state.go", Symbol: "Rebuild"}
	enforcement := DomainFactEnforcementAnchor{Kind: "go_test", Value: "TestInvariant", CheckID: "check-tests"}
	refs := DomainFactReferences{
		Registry: KnowledgeDomainRegistry{Domains: []KnowledgeDomain{
			{DomainID: "domain-a", Status: "current"}, {DomainID: "domain-b", Status: "current"}, {DomainID: "retired", Status: "deprecated"},
		}},
		Roots:          map[string]DomainFactRoot{"src": {Role: "domain", OwnerDomainIDs: []string{"domain-a"}}},
		AcceptedLawIDs: map[string]bool{"law-a": true}, ResourceIDs: map[string]bool{"database-a": true, "cache-a": true},
		Facts:             map[string]DomainFactKind{"invariant-a": DomainFactInvariant, "data-a": DomainFactDataObject},
		RepositoryAnchors: map[DomainFactRepositoryAnchor]bool{anchor: true}, RequiredCheckAnchors: map[DomainFactEnforcementAnchor]bool{enforcement: true},
	}
	fact := DomainFact{SchemaVersion: "1.0", ID: "fact-a", Kind: kind, OwnerDomainID: "domain-a", OwningRoot: "src", Standing: "observed"}
	switch kind {
	case DomainFactInvariant:
		fact.Invariant = &DomainInvariantFact{Statement: "Only the owner writes state.", AppliesToDomainIDs: []string{"domain-a", "domain-b"}, EnforcedBy: []DomainFactEnforcementAnchor{enforcement}}
	case DomainFactDataObject:
		fact.DataObject = &DomainDataObjectFact{Name: "state_rows", ResourceID: "database-a", WriterDomainIDs: []string{"domain-a"}, ReaderDomainIDs: []string{"domain-b"}, InvariantIDs: []string{"invariant-a"}}
	case DomainFactStateAuthority:
		fact.StateAuthority = &DomainStateAuthorityFact{State: "account state", AuthoritativeResourceID: "database-a", DerivedCopies: []DomainFactDerivedCopy{{ResourceID: "cache-a", RebuildRoute: anchor}}, DisagreementBehavior: "Rebuild the cache from the authoritative database."}
	case DomainFactConfigCoupling:
		fact.ConfigCoupling = &DomainConfigCouplingFact{Setting: "CACHE_LIMIT", Source: "process environment", ReadBy: anchor, Effect: "Bounds cached rows.", Environments: []string{"test", "production"}}
	}
	return fact, refs
}

func marshalDomainFact(t *testing.T, fact DomainFact) []byte {
	t.Helper()
	data, err := json.Marshal(fact)
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func TestDomainFactExplicitUnverifiedReferences(t *testing.T) {
	for _, kind := range []DomainFactKind{DomainFactInvariant, DomainFactDataObject, DomainFactStateAuthority, DomainFactConfigCoupling} {
		t.Run(string(kind), func(t *testing.T) {
			fact, refs := domainFactFixture(kind)
			refs.RepositoryAnchors, refs.RequiredCheckAnchors = nil, nil
			refs.ValidationMode = DomainFactUnverifiedReferences
			refs.ProjectIDs = map[string]bool{"project-a": true}
			refs.RequiredCheckIDs = map[string]bool{"check-tests": true}
			if _, err := ParseDomainFact(fact.ID+".json", marshalDomainFact(t, fact), refs); err != nil {
				t.Fatal(err)
			}
			if len(refs.RepositoryAnchors) != 0 || len(refs.RequiredCheckAnchors) != 0 {
				t.Fatal("structural validation manufactured resolution evidence")
			}
			if kind != DomainFactDataObject {
				refs.ValidationMode = DomainFactResolvedReferences
				if _, err := ParseDomainFact(fact.ID+".json", marshalDomainFact(t, fact), refs); err == nil {
					t.Fatal("default resolved-input API admitted unresolved anchors")
				}
			}
		})
	}
	for _, name := range []string{"unknown check", "unknown kind", "foreign project", "malformed code path", "unknown mode"} {
		t.Run(name, func(t *testing.T) {
			kind := DomainFactInvariant
			if name == "foreign project" || name == "malformed code path" {
				kind = DomainFactConfigCoupling
			}
			fact, refs := domainFactFixture(kind)
			refs.ValidationMode = DomainFactUnverifiedReferences
			refs.ProjectIDs = map[string]bool{"project-a": true}
			refs.RequiredCheckIDs = map[string]bool{"check-tests": true}
			switch name {
			case "unknown check":
				fact.Invariant.EnforcedBy[0].CheckID = "unknown"
			case "unknown kind":
				fact.Invariant.EnforcedBy[0].Kind = "code"
			case "foreign project":
				fact.ConfigCoupling.ReadBy.ProjectID = "foreign"
			case "malformed code path":
				fact.ConfigCoupling.ReadBy.Path = "../source.go"
			case "unknown mode":
				refs.ValidationMode = 99
			}
			if _, err := ParseDomainFact(fact.ID+".json", marshalDomainFact(t, fact), refs); err == nil {
				t.Fatal("unverified mode bypassed structural reference validation")
			}
		})
	}
}

func TestDomainFactKindsAndStanding(t *testing.T) {
	for _, kind := range []DomainFactKind{DomainFactInvariant, DomainFactDataObject, DomainFactStateAuthority, DomainFactConfigCoupling} {
		for _, standing := range []string{"observed", "law"} {
			t.Run(string(kind)+"/"+standing, func(t *testing.T) {
				fact, refs := domainFactFixture(kind)
				fact.Standing = standing
				if standing == "law" {
					fact.GoverningLawIDs = []string{"law-a"}
				}
				got, err := ParseDomainFact("fact-a.json", marshalDomainFact(t, fact), refs)
				if err != nil || !reflect.DeepEqual(got, fact) {
					t.Fatalf("got %+v, %v; want %+v", got, err, fact)
				}
			})
		}
	}
	for _, kind := range []DomainFactKind{DomainFactDataObject, DomainFactStateAuthority} {
		fact, refs := domainFactFixture(kind)
		if kind == DomainFactDataObject {
			fact.DataObject.WriterDomainIDs = []string{}
			fact.DataObject.ReaderDomainIDs = []string{}
			fact.DataObject.InvariantIDs = []string{}
		} else {
			fact.StateAuthority.DerivedCopies = []DomainFactDerivedCopy{}
		}
		if _, err := ParseDomainFact("fact-a.json", marshalDomainFact(t, fact), refs); err != nil {
			t.Fatalf("explicit empty relationship list: %v", err)
		}
	}
}

func TestDomainFactRejectsInvalidReferencesAndPayloads(t *testing.T) {
	tests := []struct {
		name string
		kind DomainFactKind
		edit func(*DomainFact, *DomainFactReferences)
	}{
		{"no owner", DomainFactInvariant, func(f *DomainFact, _ *DomainFactReferences) { f.OwnerDomainID = "" }},
		{"unknown owner", DomainFactInvariant, func(f *DomainFact, _ *DomainFactReferences) { f.OwnerDomainID = "unknown" }},
		{"deprecated owner", DomainFactInvariant, func(f *DomainFact, _ *DomainFactReferences) { f.OwnerDomainID = "retired" }},
		{"unknown root", DomainFactInvariant, func(f *DomainFact, _ *DomainFactReferences) { f.OwningRoot = "other" }},
		{"composition root", DomainFactInvariant, func(_ *DomainFact, r *DomainFactReferences) { r.Roots["src"] = DomainFactRoot{Role: "composition"} }},
		{"test support root", DomainFactInvariant, func(_ *DomainFact, r *DomainFactReferences) { r.Roots["src"] = DomainFactRoot{Role: "test_support"} }},
		{"zero root owners", DomainFactInvariant, func(_ *DomainFact, r *DomainFactReferences) { r.Roots["src"] = DomainFactRoot{Role: "domain"} }},
		{"two root owners", DomainFactInvariant, func(_ *DomainFact, r *DomainFactReferences) {
			r.Roots["src"] = DomainFactRoot{Role: "domain", OwnerDomainIDs: []string{"domain-a", "domain-b"}}
		}},
		{"wrong root owner", DomainFactInvariant, func(_ *DomainFact, r *DomainFactReferences) {
			r.Roots["src"] = DomainFactRoot{Role: "domain", OwnerDomainIDs: []string{"domain-b"}}
		}},
		{"mixed missing owner", DomainFactInvariant, func(_ *DomainFact, r *DomainFactReferences) {
			r.Roots["src"] = DomainFactRoot{Role: "mixed", OwnerDomainIDs: []string{"domain-b", "unknown"}}
		}},
		{"duplicate mixed owner", DomainFactInvariant, func(_ *DomainFact, r *DomainFactReferences) {
			r.Roots["src"] = DomainFactRoot{Role: "mixed", OwnerDomainIDs: []string{"domain-a", "domain-a"}}
		}},
		{"law without laws", DomainFactInvariant, func(f *DomainFact, _ *DomainFactReferences) { f.Standing = "law" }},
		{"unaccepted law", DomainFactInvariant, func(f *DomainFact, _ *DomainFactReferences) {
			f.Standing = "law"
			f.GoverningLawIDs = []string{"draft-law"}
		}},
		{"observed cites laws", DomainFactInvariant, func(f *DomainFact, _ *DomainFactReferences) { f.GoverningLawIDs = []string{"law-a"} }},
		{"unknown standing", DomainFactInvariant, func(f *DomainFact, _ *DomainFactReferences) { f.Standing = "approved" }},
		{"unknown kind", DomainFactInvariant, func(f *DomainFact, _ *DomainFactReferences) { f.Kind = "concern" }},
		{"missing variant", DomainFactInvariant, func(f *DomainFact, _ *DomainFactReferences) { f.Invariant = nil }},
		{"unrelated variant", DomainFactInvariant, func(f *DomainFact, _ *DomainFactReferences) {
			other, _ := domainFactFixture(DomainFactDataObject)
			f.DataObject = other.DataObject
		}},
		{"blank statement", DomainFactInvariant, func(f *DomainFact, _ *DomainFactReferences) { f.Invariant.Statement = " " }},
		{"unknown applies", DomainFactInvariant, func(f *DomainFact, _ *DomainFactReferences) { f.Invariant.AppliesToDomainIDs = []string{"unknown"} }},
		{"duplicate applies", DomainFactInvariant, func(f *DomainFact, _ *DomainFactReferences) {
			f.Invariant.AppliesToDomainIDs = []string{"domain-a", "domain-a"}
		}},
		{"no enforcement", DomainFactInvariant, func(f *DomainFact, _ *DomainFactReferences) { f.Invariant.EnforcedBy = []DomainFactEnforcementAnchor{} }},
		{"bad anchor kind", DomainFactInvariant, func(f *DomainFact, _ *DomainFactReferences) { f.Invariant.EnforcedBy[0].Kind = "file" }},
		{"unresolved anchor", DomainFactInvariant, func(f *DomainFact, _ *DomainFactReferences) { f.Invariant.EnforcedBy[0].Value = "TestAbsent" }},
		{"undeclared check", DomainFactInvariant, func(f *DomainFact, _ *DomainFactReferences) { f.Invariant.EnforcedBy[0].CheckID = "other-check" }},
		{"nonrequired check", DomainFactInvariant, func(_ *DomainFact, r *DomainFactReferences) { r.RequiredCheckAnchors = nil }},
		{"duplicate anchor", DomainFactInvariant, func(f *DomainFact, _ *DomainFactReferences) {
			f.Invariant.EnforcedBy = append(f.Invariant.EnforcedBy, f.Invariant.EnforcedBy[0])
		}},
		{"unknown data resource", DomainFactDataObject, func(f *DomainFact, _ *DomainFactReferences) { f.DataObject.ResourceID = "unknown" }},
		{"unknown writer", DomainFactDataObject, func(f *DomainFact, _ *DomainFactReferences) { f.DataObject.WriterDomainIDs = []string{"unknown"} }},
		{"unknown reader", DomainFactDataObject, func(f *DomainFact, _ *DomainFactReferences) { f.DataObject.ReaderDomainIDs = []string{"unknown"} }},
		{"dangling invariant", DomainFactDataObject, func(f *DomainFact, _ *DomainFactReferences) { f.DataObject.InvariantIDs = []string{"missing"} }},
		{"wrong fact kind", DomainFactDataObject, func(f *DomainFact, _ *DomainFactReferences) { f.DataObject.InvariantIDs = []string{"data-a"} }},
		{"unknown authoritative resource", DomainFactStateAuthority, func(f *DomainFact, _ *DomainFactReferences) { f.StateAuthority.AuthoritativeResourceID = "unknown" }},
		{"unknown derived resource", DomainFactStateAuthority, func(f *DomainFact, _ *DomainFactReferences) { f.StateAuthority.DerivedCopies[0].ResourceID = "unknown" }},
		{"derived is authoritative", DomainFactStateAuthority, func(f *DomainFact, _ *DomainFactReferences) {
			f.StateAuthority.DerivedCopies[0].ResourceID = "database-a"
		}},
		{"duplicate derived resource", DomainFactStateAuthority, func(f *DomainFact, _ *DomainFactReferences) {
			f.StateAuthority.DerivedCopies = append(f.StateAuthority.DerivedCopies, f.StateAuthority.DerivedCopies[0])
		}},
		{"missing rebuild", DomainFactStateAuthority, func(f *DomainFact, _ *DomainFactReferences) {
			f.StateAuthority.DerivedCopies[0].RebuildRoute.Symbol = ""
		}},
		{"unknown rebuild", DomainFactStateAuthority, func(f *DomainFact, _ *DomainFactReferences) {
			f.StateAuthority.DerivedCopies[0].RebuildRoute.Symbol = "Missing"
		}},
		{"unknown project", DomainFactConfigCoupling, func(f *DomainFact, _ *DomainFactReferences) { f.ConfigCoupling.ReadBy.ProjectID = "other-project" }},
		{"unknown reading symbol", DomainFactConfigCoupling, func(f *DomainFact, _ *DomainFactReferences) { f.ConfigCoupling.ReadBy.Symbol = "Missing" }},
		{"no environments", DomainFactConfigCoupling, func(f *DomainFact, _ *DomainFactReferences) { f.ConfigCoupling.Environments = []string{} }},
		{"duplicate environment", DomainFactConfigCoupling, func(f *DomainFact, _ *DomainFactReferences) { f.ConfigCoupling.Environments = []string{"test", "test"} }},
		{"blank source", DomainFactConfigCoupling, func(f *DomainFact, _ *DomainFactReferences) { f.ConfigCoupling.Source = "" }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			fact, refs := domainFactFixture(test.kind)
			test.edit(&fact, &refs)
			data := marshalDomainFact(t, fact)
			// A declaration decoded without semantic validation accepts these cases.
			var unvalidated DomainFact
			if err := json.Unmarshal(data, &unvalidated); err != nil {
				t.Fatalf("invalid test setup: %v", err)
			}
			got, err := ParseDomainFact("fact-a.json", data, refs)
			if err == nil || !reflect.DeepEqual(got, DomainFact{}) {
				t.Fatalf("invalid fact accepted or partial fact returned: %+v, %v", got, err)
			}
		})
	}
	for _, owners := range [][]string{{"domain-a", "domain-b"}, {"domain-b", "domain-a"}} {
		fact, refs := domainFactFixture(DomainFactInvariant)
		refs.Roots["src"] = DomainFactRoot{Role: "mixed", OwnerDomainIDs: owners}
		if _, err := ParseDomainFact("fact-a.json", marshalDomainFact(t, fact), refs); err != nil {
			t.Fatalf("valid mixed ownership: %v", err)
		}
	}
}

func TestDomainFactStrictJSON(t *testing.T) {
	fact, refs := domainFactFixture(DomainFactInvariant)
	valid := marshalDomainFact(t, fact)
	tests := map[string][]byte{
		"unknown field":       bytes.Replace(valid, []byte(`"schema_version"`), []byte(`"extra":1,"schema_version"`), 1),
		"duplicate key":       bytes.Replace(valid, []byte(`"id":"fact-a"`), []byte(`"id":"fact-a","id":"fact-a"`), 1),
		"nested duplicate":    bytes.Replace(valid, []byte(`"check_id":"check-tests"`), []byte(`"check_id":"check-tests","check_id":"check-tests"`), 1),
		"nested unknown":      bytes.Replace(valid, []byte(`"check_id":"check-tests"`), []byte(`"check_id":"check-tests","body":"no"`), 1),
		"case folded field":   bytes.Replace(valid, []byte(`"owner_domain_id"`), []byte(`"OWNER_DOMAIN_ID"`), 1),
		"multiple owners":     bytes.Replace(valid, []byte(`"owner_domain_id":"domain-a"`), []byte(`"owner_domain_id":["domain-a","domain-b"]`), 1),
		"null owner":          bytes.Replace(valid, []byte(`"owner_domain_id":"domain-a"`), []byte(`"owner_domain_id":null`), 1),
		"null optional":       bytes.Replace(valid, []byte(`"standing":"observed"`), []byte(`"standing":"observed","governing_law_ids":null`), 1),
		"empty observed laws": bytes.Replace(valid, []byte(`"standing":"observed"`), []byte(`"standing":"observed","governing_law_ids":[]`), 1),
		"trailing value":      append(append([]byte{}, valid...), []byte(` {}`)...),
		"trailing garbage":    append(append([]byte{}, valid...), []byte(`garbage`)...),
		"invalid utf8":        append(append([]byte{}, valid...), 0xff),
		"null":                []byte(`null`), "array": []byte(`[]`), "empty": nil,
	}
	for name, data := range tests {
		t.Run(name, func(t *testing.T) {
			if _, err := ParseDomainFact("fact-a.json", data, refs); err == nil {
				t.Fatal("accepted malformed shard")
			}
		})
	}
	for _, name := range []string{"other.json", "dir/fact-a.json", "../fact-a.json", "/fact-a.json"} {
		if _, err := ParseDomainFact(name, valid, refs); err == nil {
			t.Errorf("accepted shard basename %q", name)
		}
	}
	if _, err := ParseDomainFact("fact-a.json", append(valid, []byte(" \n\t")...), refs); err != nil {
		t.Fatalf("legal trailing whitespace: %v", err)
	}
}

func TestDomainFactEveryFieldIsRequired(t *testing.T) {
	for _, kind := range []DomainFactKind{DomainFactInvariant, DomainFactDataObject, DomainFactStateAuthority, DomainFactConfigCoupling} {
		fact, refs := domainFactFixture(kind)
		var object map[string]json.RawMessage
		if err := json.Unmarshal(marshalDomainFact(t, fact), &object); err != nil {
			t.Fatal(err)
		}
		for _, field := range []string{"schema_version", "id", "kind", "owner_domain_id", "owning_root", "standing", string(kind)} {
			value := object[field]
			delete(object, field)
			data, _ := json.Marshal(object)
			if _, err := ParseDomainFact("fact-a.json", data, refs); err == nil {
				t.Errorf("%s accepted missing %s", kind, field)
			}
			object[field] = value
		}
		var payload map[string]json.RawMessage
		if err := json.Unmarshal(object[string(kind)], &payload); err != nil {
			t.Fatal(err)
		}
		for field, value := range payload {
			delete(payload, field)
			object[string(kind)], _ = json.Marshal(payload)
			data, _ := json.Marshal(object)
			if _, err := ParseDomainFact("fact-a.json", data, refs); err == nil {
				t.Errorf("%s accepted missing payload field %s", kind, field)
			}
			payload[field] = value
		}
	}
}

func TestDomainFactBounds(t *testing.T) {
	fact, refs := domainFactFixture(DomainFactInvariant)
	for _, text := range []string{strings.Repeat("a", DomainFactMaxTextBytes), strings.Repeat("é", DomainFactMaxTextBytes/2)} {
		fact.Invariant.Statement = text
		if _, err := ParseDomainFact("fact-a.json", marshalDomainFact(t, fact), refs); err != nil {
			t.Fatal(err)
		}
		fact.Invariant.Statement += "a"
		if _, err := ParseDomainFact("fact-a.json", marshalDomainFact(t, fact), refs); err == nil {
			t.Fatal("accepted oversized UTF-8 statement")
		}
	}
	fact, refs = domainFactFixture(DomainFactInvariant)
	valid := marshalDomainFact(t, fact)
	data := append(append([]byte{}, valid...), bytes.Repeat([]byte(" "), DomainFactMaxBytes-len(valid))...)
	if _, err := ParseDomainFact("fact-a.json", data, refs); err != nil {
		t.Fatalf("at byte bound: %v", err)
	}
	if _, err := ParseDomainFact("fact-a.json", append(data, ' '), refs); err == nil {
		t.Fatal("accepted oversized shard")
	}
	fact.Invariant = nil
	fact.Kind = DomainFactConfigCoupling
	config, _ := domainFactFixture(DomainFactConfigCoupling)
	fact.ConfigCoupling = config.ConfigCoupling
	fact.ConfigCoupling.Environments = make([]string, DomainFactMaxItems+1)
	for i := range fact.ConfigCoupling.Environments {
		fact.ConfigCoupling.Environments[i] = strings.Repeat("x", i+1)
	}
	if _, err := ParseDomainFact("fact-a.json", marshalDomainFact(t, fact), refs); err == nil {
		t.Fatal("accepted oversized environment list")
	}
	fact.ConfigCoupling.Environments = fact.ConfigCoupling.Environments[:DomainFactMaxItems]
	if _, err := ParseDomainFact("fact-a.json", marshalDomainFact(t, fact), refs); err != nil {
		t.Fatalf("at list bound: %v", err)
	}
}

func TestDomainFactSchemaMatchesModel(t *testing.T) {
	data, err := os.ReadFile("../../contracts/domain-fact.v1.schema.json")
	if err != nil {
		t.Fatal(err)
	}
	var schema struct {
		Properties map[string]json.RawMessage `json:"properties"`
	}
	if err := json.Unmarshal(data, &schema); err != nil {
		t.Fatal(err)
	}
	model := reflect.TypeFor[DomainFact]()
	if len(schema.Properties) != model.NumField() {
		t.Fatal("schema and model field counts differ")
	}
	for i := 0; i < model.NumField(); i++ {
		name, _, _ := strings.Cut(model.Field(i).Tag.Get("json"), ",")
		if schema.Properties[name] == nil {
			t.Errorf("schema omits %s", name)
		}
	}
}

func TestDomainFactResolvedInputsDoNotWaiveShape(t *testing.T) {
	for _, path := range []string{"../src", "/src", "src/../src", "src//nested", "src/", "src/*", "src\\state.go", "."} {
		t.Run(path, func(t *testing.T) {
			fact, refs := domainFactFixture(DomainFactInvariant)
			fact.OwningRoot = path
			refs.Roots[path] = refs.Roots["src"]
			if _, err := ParseDomainFact("fact-a.json", marshalDomainFact(t, fact), refs); err == nil {
				t.Fatal("resolved root bypassed normalized path validation")
			}
			fact, refs = domainFactFixture(DomainFactConfigCoupling)
			fact.ConfigCoupling.ReadBy.Path = path
			refs.RepositoryAnchors[fact.ConfigCoupling.ReadBy] = true
			if _, err := ParseDomainFact("fact-a.json", marshalDomainFact(t, fact), refs); err == nil {
				t.Fatal("resolved anchor bypassed normalized path validation")
			}
		})
	}
	for _, kind := range []string{"adapter_test", "generated", "go_test", "scenario", "validator", "file"} {
		fact, refs := domainFactFixture(DomainFactInvariant)
		fact.Invariant.EnforcedBy[0].Kind = kind
		refs.RequiredCheckAnchors[fact.Invariant.EnforcedBy[0]] = true
		_, err := ParseDomainFact("fact-a.json", marshalDomainFact(t, fact), refs)
		if (err == nil) != (kind != "file") {
			t.Errorf("anchor kind %s: %v", kind, err)
		}
	}
	for _, id := range []string{strings.Repeat("a", 128), strings.Repeat("a", 129), "../escape"} {
		fact, refs := domainFactFixture(DomainFactInvariant)
		fact.ID = id
		_, err := ParseDomainFact(id+".json", marshalDomainFact(t, fact), refs)
		if (err == nil) != (len(id) == 128) {
			t.Errorf("fact ID %q: %v", id, err)
		}
	}
}

func TestDomainFactRelationshipCountBounds(t *testing.T) {
	for _, kind := range []DomainFactKind{DomainFactInvariant, DomainFactDataObject, DomainFactStateAuthority} {
		t.Run(string(kind), func(t *testing.T) {
			fact, refs := domainFactFixture(kind)
			for n := 0; n <= DomainFactMaxItems; n++ {
				id := fmt.Sprintf("reference-%d", n)
				switch kind {
				case DomainFactInvariant:
					if n == 0 {
						fact.Invariant.EnforcedBy = nil
					}
					anchor := DomainFactEnforcementAnchor{Kind: "go_test", Value: id, CheckID: "check-tests"}
					refs.RequiredCheckAnchors[anchor] = true
					fact.Invariant.EnforcedBy = append(fact.Invariant.EnforcedBy, anchor)
				case DomainFactDataObject:
					if n == 0 {
						fact.DataObject.InvariantIDs = nil
					}
					refs.Facts[id] = DomainFactInvariant
					fact.DataObject.InvariantIDs = append(fact.DataObject.InvariantIDs, id)
				case DomainFactStateAuthority:
					if n == 0 {
						fact.StateAuthority.DerivedCopies = nil
					}
					refs.ResourceIDs[id] = true
					anchor := DomainFactRepositoryAnchor{ProjectID: "project-a", Path: "src/state.go", Symbol: id}
					refs.RepositoryAnchors[anchor] = true
					fact.StateAuthority.DerivedCopies = append(fact.StateAuthority.DerivedCopies, DomainFactDerivedCopy{ResourceID: id, RebuildRoute: anchor})
				}
				_, err := ParseDomainFact("fact-a.json", marshalDomainFact(t, fact), refs)
				if (err == nil) != (n < DomainFactMaxItems) {
					t.Fatalf("%d relationships: %v", n+1, err)
				}
			}
		})
	}
}

func TestDomainFactPreservesExistingReferenceIdentities(t *testing.T) {
	fact, refs := domainFactFixture(DomainFactDataObject)
	domainID := strings.Repeat("é", maxManifestID)
	refs.Registry.Domains = append(refs.Registry.Domains, KnowledgeDomain{DomainID: domainID, Status: "current"})
	refs.Roots["src"] = DomainFactRoot{Role: "domain", OwnerDomainIDs: []string{domainID}}
	fact.OwnerDomainID = domainID
	fact.DataObject.WriterDomainIDs = []string{domainID}
	resourceID := strings.Repeat("r", 250) + "/store"
	refs.ResourceIDs[resourceID] = true
	fact.DataObject.ResourceID = resourceID
	fact.Standing = "law"
	fact.GoverningLawIDs = []string{"spec/domain behavior"}
	refs.AcceptedLawIDs["spec/domain behavior"] = true
	if _, err := ParseDomainFact("fact-a.json", marshalDomainFact(t, fact), refs); err != nil {
		t.Fatalf("rejected identities admitted by existing owners: %v", err)
	}
	fact.DataObject.ResourceID += "r"
	refs.ResourceIDs[fact.DataObject.ResourceID] = true
	if _, err := ParseDomainFact("fact-a.json", marshalDomainFact(t, fact), refs); err == nil {
		t.Fatal("accepted resource identity beyond its existing 256-byte bound")
	}
}
