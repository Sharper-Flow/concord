package store

// The embedded cohort and Python reader admit the same closed asset shape.

import (
	"strings"
	"testing"
)

func authorityDefaultsFixture() []byte {
	return []byte(`{
  "schema": "knowledge-authority-defaults.v1",
  "application_sha": "fe69821f724bc8d40ec2730c9fd390bd80f1f527",
  "vehicle": {"work_id": "work-8d7bd788b2890355460bb5b7", "contract_version": 1},
  "members": [
    {"record_id": "CD-0002", "kind": "decision"},
    {"record_id": "CD-0003", "kind": "decision"}
  ]
}`)
}

func TestKnowledgeAuthorityDefaultsAssetParsesTheClosedShape(t *testing.T) {
	t.Parallel()
	defaults, err := knowledgeAuthorityDefaultsAsset(authorityDefaultsFixture())
	if err != nil {
		t.Fatalf("the closed asset shape must parse: %v", err)
	}
	if defaults.vehicleWorkID != "work-8d7bd788b2890355460bb5b7" || defaults.vehicleContractVersion != 1 || len(defaults.members) != 2 {
		t.Fatalf("parsed defaults = %+v", defaults)
	}
}

func TestKnowledgeAuthorityDefaultsAssetRefusesEveryDefect(t *testing.T) {
	t.Parallel()
	cases := map[string]string{
		"unknown root key":      `{"schema":"knowledge-authority-defaults.v1","application_sha":"fe69821f724bc8d40ec2730c9fd390bd80f1f527","vehicle":{"work_id":"work-8d7bd788b2890355460bb5b7","contract_version":1},"members":[{"record_id":"CD-0002","kind":"decision"}],"extra":1}`,
		"wrong schema constant": strings.Replace(authorityDefaultsFixtureString(), "knowledge-authority-defaults.v1", "knowledge-authority-defaults.v2", 1),
		"sha not a git object":  strings.Replace(authorityDefaultsFixtureString(), "fe69821f724bc8d40ec2730c9fd390bd80f1f527", "FE69821F724BC8D40EC2730C9FD390BD80F1F527", 1),
		"vehicle work id shape": strings.Replace(authorityDefaultsFixtureString(), "work-8d7bd788b2890355460bb5b7", "vehicle-authority", 1),
		"boolean version":       strings.Replace(authorityDefaultsFixtureString(), `"contract_version": 1`, `"contract_version": true`, 1),
		"zero version":          strings.Replace(authorityDefaultsFixtureString(), `"contract_version": 1`, `"contract_version": 0`, 1),
		"empty members": strings.Replace(authorityDefaultsFixtureString(), `"members": [
    {"record_id": "CD-0002", "kind": "decision"},
    {"record_id": "CD-0003", "kind": "decision"}
  ]`, `"members": []`, 1),
		"unknown member key": strings.Replace(authorityDefaultsFixtureString(), `{"record_id": "CD-0002", "kind": "decision"}`, `{"record_id": "CD-0002", "kind": "decision", "tier": "legislated"}`, 1),
		"unsorted members": strings.Replace(authorityDefaultsFixtureString(), `{"record_id": "CD-0002", "kind": "decision"},
    {"record_id": "CD-0003", "kind": "decision"}`, `{"record_id": "CD-0003", "kind": "decision"},
    {"record_id": "CD-0002", "kind": "decision"}`, 1),
		"duplicate member": strings.Replace(authorityDefaultsFixtureString(), `{"record_id": "CD-0002", "kind": "decision"},
    {"record_id": "CD-0003", "kind": "decision"}`, `{"record_id": "CD-0002", "kind": "decision"},
    {"record_id": "CD-0002", "kind": "decision"}`, 1),
		"member field not clean": strings.Replace(authorityDefaultsFixtureString(), `"CD-0002"`, `" CD-0002"`, 1),
		"malformed json":         `{`,
	}
	for name, document := range cases {
		if _, err := knowledgeAuthorityDefaultsAsset([]byte(document)); err == nil {
			t.Fatalf("%s: the asset parser must refuse, never skip", name)
		}
	}
}

func authorityDefaultsFixtureString() string {
	return string(authorityDefaultsFixture())
}

func TestKnowledgeAuthorityDefaultsAssetRefusesTrailingData(t *testing.T) {
	t.Parallel()
	for _, suffix := range []string{"{}", "null", "garbage"} {
		if _, err := knowledgeAuthorityDefaultsAsset(append(authorityDefaultsFixture(), []byte(suffix)...)); err == nil {
			t.Fatalf("the asset must contain exactly one JSON document, accepted suffix %q", suffix)
		}
	}
}

func TestValidateKnowledgeAuthorityDefaultEnforcesTheFrozenClaim(t *testing.T) {
	t.Parallel()
	vehicle := KnowledgeAuthority{Tier: "legislated", LegislatedBy: "work-8d7bd788b2890355460bb5b7", ContractVersion: 1}
	member := KnowledgeRecord{ID: "CD-0002", Kind: "decision", Authority: vehicle}
	if err := validateKnowledgeAuthorityDefault(member); err != nil {
		t.Fatalf("a frozen member at the vehicle version carries no refusal: %v", err)
	}
	wrongVersion := member
	wrongVersion.Authority.ContractVersion = 2
	if err := validateKnowledgeAuthorityDefault(wrongVersion); err == nil {
		t.Fatal("a member at any other version must refuse")
	}
	outside := member
	outside.ID = "CD-9999"
	if err := validateKnowledgeAuthorityDefault(outside); err == nil {
		t.Fatal("a non-member vehicle claim must refuse")
	}
	nonVehicle := member
	nonVehicle.Authority.LegislatedBy = "work-0f1e2d3c4b5a6978"
	if err := validateKnowledgeAuthorityDefault(nonVehicle); err != nil {
		t.Fatalf("a non-vehicle reference carries no cohort obligation: %v", err)
	}
}
