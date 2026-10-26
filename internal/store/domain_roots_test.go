package store

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"reflect"
	"strings"
	"testing"
)

func domainRootsRegistry() KnowledgeDomainRegistry {
	return KnowledgeDomainRegistry{Domains: []KnowledgeDomain{
		{DomainID: "alpha", Status: "current"},
		{DomainID: "beta", Status: "current"},
		{DomainID: "retired", Status: "deprecated"},
	}}
}

func TestDomainRootsExplicitRoles(t *testing.T) {
	document, err := parseKnowledgePackageMap([]byte(`{"schema_version":"1.1","packages":[
		{"path":"src/support","role":"test_support"},
		{"path":"src/wiring","role":"composition"},
		{"path":"src/mixed","role":"mixed","domain_ids":["beta","alpha"]},
		{"path":"src/domain","role":"domain","owner_domain_id":"alpha"}
	]}`), domainRootsRegistry())
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]DomainFactRoot{
		"src/domain":  {Role: "domain", OwnerDomainIDs: []string{"alpha"}},
		"src/mixed":   {Role: "mixed", OwnerDomainIDs: []string{"alpha", "beta"}},
		"src/support": {Role: "test_support"},
		"src/wiring":  {Role: "composition"},
	}
	refs := DomainFactReferences{Registry: domainRootsRegistry(), Roots: document.domainFactRoots()}
	if !reflect.DeepEqual(refs.Roots, want) {
		t.Fatalf("roots = %#v, want %#v", refs.Roots, want)
	}
	if document.Packages[0].Path != "src/domain" || len(document.Packages[0].DomainIDs) != 0 {
		t.Fatalf("domain root must not acquire multi-Domain ownership: %#v", document.Packages)
	}
	refs.Roots["src/mixed"].OwnerDomainIDs[0] = "changed"
	if document.Packages[1].DomainIDs[0] != "alpha" {
		t.Fatal("helper aliases parsed ownership")
	}
	encoded, err := json.Marshal(document)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := parseKnowledgePackageMap(encoded, domainRootsRegistry()); err != nil {
		t.Fatalf("1.1 serializer must preserve role shape: %s: %v", encoded, err)
	}
}

func TestDomainRootsHistoricalEncodingAndHash(t *testing.T) {
	const canonical = `{"schema_version":"1.0","packages":[{"path":"src/alpha","domain_ids":["alpha"]},{"path":"src/beta","domain_ids":["alpha","beta"]}]}`
	document, err := parseKnowledgePackageMap([]byte(`{"schema_version":"1.0","packages":[{"path":"src/beta","domain_ids":["beta","alpha"]},{"path":"src/alpha","domain_ids":["alpha"]}]}`), domainRootsRegistry())
	if err != nil {
		t.Fatal(err)
	}
	encoded, err := json.Marshal(document)
	if err != nil || string(encoded) != canonical {
		t.Fatalf("historical encoding changed: %s, %v", encoded, err)
	}
	sum := sha256.Sum256([]byte(canonical))
	if got, want := knowledgePackageMapContentHash(document), "sha256:"+hex.EncodeToString(sum[:]); got != want {
		t.Fatalf("historical hash = %s, want %s", got, want)
	}
	for _, root := range document.domainFactRoots() {
		if root.Role != "mixed" {
			t.Fatalf("historical root claims exclusive ownership: %#v", root)
		}
	}
}

func TestDomainRootsFactReferenceInteroperation(t *testing.T) {
	anchor := DomainFactEnforcementAnchor{Kind: "go_test", Value: "TestOwner", CheckID: "check-owner"}
	refs := DomainFactReferences{
		Registry:             domainRootsRegistry(),
		RequiredCheckAnchors: map[DomainFactEnforcementAnchor]bool{anchor: true},
	}
	fact := DomainFact{
		SchemaVersion: DomainFactSchemaVersion, ID: "fact-owner", Kind: DomainFactInvariant,
		OwnerDomainID: "alpha", OwningRoot: "src", Standing: "observed",
		Invariant: &DomainInvariantFact{Statement: "Only the owner writes.", AppliesToDomainIDs: []string{"alpha"}, EnforcedBy: []DomainFactEnforcementAnchor{anchor}},
	}
	data, err := json.Marshal(fact)
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		version, entry string
		ownsFacts      bool
	}{
		{"1.1", `{"path":"src","role":"domain","owner_domain_id":"alpha"}`, true},
		{"1.1", `{"path":"src","role":"mixed","domain_ids":["alpha","beta"]}`, true},
		{"1.1", `{"path":"src","role":"domain","owner_domain_id":"beta"}`, false},
		{"1.1", `{"path":"src","role":"composition"}`, false},
		{"1.1", `{"path":"src","role":"test_support"}`, false},
		{"1.0", `{"path":"src","domain_ids":["alpha","beta"]}`, true},
		{"1.0", `{"path":"src","domain_ids":["alpha"]}`, false},
	} {
		document, err := parseKnowledgePackageMap(fmt.Appendf(nil, `{"schema_version":%q,"packages":[%s]}`, tc.version, tc.entry), refs.Registry)
		if err != nil {
			t.Fatal(err)
		}
		refs.Roots = document.domainFactRoots()
		if _, err := ParseDomainFact("fact-owner.json", data, refs); (err == nil) != tc.ownsFacts {
			t.Errorf("%s %s: owns facts=%v, err=%v", tc.version, tc.entry, tc.ownsFacts, err)
		}
	}
}

func TestDomainRootsEmptyMapOnlyInShapeVersion(t *testing.T) {
	for _, version := range []string{"1.0", "1.1"} {
		_, err := parseKnowledgePackageMap(fmt.Appendf(nil, `{"schema_version":%q,"packages":[]}`, version), domainRootsRegistry())
		if (err == nil) != (version == "1.1") {
			t.Fatalf("version %s empty map: %v", version, err)
		}
	}
}

func TestDomainRootsRejectInvalidRoleOwnership(t *testing.T) {
	cases := []string{
		`{"path":"src","role":"domain"}`,
		`{"path":"src","role":"domain","owner_domain_id":""}`,
		`{"path":"src","role":"domain","owner_domain_id":["alpha","beta"]}`,
		`{"path":"src","role":"domain","owner_domain_id":"alpha","domain_ids":[]}`,
		`{"path":"src","role":"domain","owner_domain_id":null}`,
		`{"path":"src","role":"domain","owner_domain_id":"unknown"}`,
		`{"path":"src","role":"domain","owner_domain_id":"retired"}`,
		`{"path":"src","role":"mixed","domain_ids":[]}`,
		`{"path":"src","role":"mixed","domain_ids":["alpha"]}`,
		`{"path":"src","role":"mixed","domain_ids":["alpha","alpha"]}`,
		`{"path":"src","role":"mixed","domain_ids":["alpha","unknown"]}`,
		`{"path":"src","role":"mixed","domain_ids":["alpha","retired"]}`,
		`{"path":"src","role":"mixed","domain_ids":["alpha","beta"],"owner_domain_id":"alpha"}`,
		`{"path":"src","role":"mixed","domain_ids":null}`,
		`{"path":"src","role":"composition","domain_ids":[]}`,
		`{"path":"src","role":"composition","owner_domain_id":null}`,
		`{"path":"src","role":"test_support","owner_domain_id":"alpha"}`,
		`{"path":"src","role":"unknown"}`,
		`{"path":"src","domain_ids":["alpha"]}`,
		`{"path":"src","Role":"composition"}`,
		`{"path":"src","role":"composition","role":"composition"}`,
	}
	for _, entry := range cases {
		t.Run(entry, func(t *testing.T) {
			if _, err := parseKnowledgePackageMap([]byte(`{"schema_version":"1.1","packages":[`+entry+`]}`), domainRootsRegistry()); err == nil {
				t.Fatal("invalid ownership accepted")
			}
		})
	}
}

func TestDomainRootsBoundsAndStrictShape(t *testing.T) {
	registry := domainRootsRegistry()
	var domains []string
	for index := 0; index < 17; index++ {
		id := fmt.Sprintf("domain-%02d", index)
		registry.Domains = append(registry.Domains, KnowledgeDomain{DomainID: id, Status: "current"})
		domains = append(domains, id)
	}
	for _, count := range []int{16, 17} {
		ids, _ := json.Marshal(domains[:count])
		_, err := parseKnowledgePackageMap([]byte(`{"schema_version":"1.1","packages":[{"path":"src","role":"mixed","domain_ids":`+string(ids)+`}]}`), registry)
		if (err == nil) != (count == 16) {
			t.Fatalf("mixed with %d Domains: %v", count, err)
		}
	}
	for _, version := range []string{"1.0", "1.1"} {
		for _, root := range []string{"src/*.go", "src/**", "src/[ab]", "src/{a,b}", "src/", "../src", "src/../other", "/src", "src\\file", "src\x00bad"} {
			fields := `"domain_ids":["alpha"]`
			if version == "1.1" {
				fields = `"role":"composition"`
			}
			raw := fmt.Appendf(nil, `{"schema_version":%q,"packages":[{"path":%q,%s}]}`, version, root, fields)
			if _, err := parseKnowledgePackageMap(raw, registry); err == nil {
				t.Errorf("version %s accepted file/glob/invalid root %q", version, root)
			}
		}
	}
	for _, raw := range []string{
		`{"schema_version":"1.1","packages":null}`,
		`{"schema_version":"1.1"}`,
		`{"schema_version":"1.1","Packages":[]}`,
		`{"schema_version":"1.1","packages":[]} {}`,
		`{"schema_version":"1.0","packages":[{"path":"src","domain_ids":["alpha"],"role":""}]}`,
		`{"schema_version":"1.1","packages":[{"path":"src","role":"composition"},{"path":"src","role":"composition"}]}`,
		strings.Repeat(" ", maxPackageMapBytes+1),
	} {
		if _, err := parseKnowledgePackageMap([]byte(raw), registry); err == nil {
			t.Fatalf("invalid map accepted: %.100s", raw)
		}
	}
}
