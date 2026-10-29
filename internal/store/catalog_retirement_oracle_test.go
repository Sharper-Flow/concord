package store

import (
	"context"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"
)

func TestCatalogRetirementSchema(t *testing.T) {
	s, err := Open(context.Background(), filepath.Join(t.TempDir(), "store.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	for _, table := range []string{"managed_resources", "resource_products", "domain_resource_attachment_sets", "domain_resource_attachment_edges"} {
		var count int
		if err := s.db.QueryRow(`SELECT count(*) FROM sqlite_schema WHERE name=?`, table).Scan(&count); err != nil {
			t.Fatal(err)
		}
		if count != 0 {
			t.Errorf("retired catalog object remains: %s", table)
		}
	}
	for _, table := range []string{"resource_claims", "domain_project_attachment_sets", "domain_project_attachment_edges", "work_items", "products"} {
		var count int
		if err := s.db.QueryRow(`SELECT count(*) FROM sqlite_schema WHERE type='table' AND name=?`, table).Scan(&count); err != nil {
			t.Fatal(err)
		}
		if count != 1 {
			t.Errorf("independent current object missing: %s", table)
		}
	}
}

func TestCatalogFreeFactSources(t *testing.T) {
	refs := DomainFactReferences{
		Registry:       KnowledgeDomainRegistry{Domains: []KnowledgeDomain{{DomainID: "state-owner", Status: "current"}}},
		Roots:          map[string]DomainFactRoot{"src": {Role: "domain", OwnerDomainIDs: []string{"state-owner"}}},
		ValidationMode: DomainFactUnverifiedReferences,
		ProjectIDs:     map[string]bool{"source-project": true},
	}
	anchor := DomainFactRepositoryAnchor{ProjectID: "source-project", Path: "src/state.go", Symbol: "Rebuild"}
	for _, kind := range []DomainFactKind{DomainFactDataObject, DomainFactStateAuthority} {
		t.Run(string(kind), func(t *testing.T) {
			fact := DomainFact{SchemaVersion: "1.0", ID: "storage-fact", Kind: kind, OwnerDomainID: "state-owner", OwningRoot: "src", Standing: "observed"}
			if kind == DomainFactDataObject {
				fact.DataObject = &DomainDataObjectFact{Name: "state_rows", ResourceID: "source-database", WriterDomainIDs: []string{"state-owner"}, ReaderDomainIDs: []string{}, InvariantIDs: []string{}}
			} else {
				fact.StateAuthority = &DomainStateAuthorityFact{State: "current state", AuthoritativeResourceID: "source-database", DerivedCopies: []DomainFactDerivedCopy{{ResourceID: "derived-cache", RebuildRoute: anchor}}, DisagreementBehavior: "Rebuild the derived cache from its source."}
			}
			parse := func(f DomainFact) error {
				data, err := json.Marshal(f)
				if err != nil {
					t.Fatal(err)
				}
				_, err = ParseDomainFact("storage-fact.json", data, refs)
				return err
			}
			if err := parse(fact); err != nil {
				t.Fatalf("fact-local source requires catalog registration: %v", err)
			}
			for _, value := range []string{"", strings.Repeat("x", 257)} {
				invalid := fact
				if kind == DomainFactDataObject {
					copy := *fact.DataObject
					copy.ResourceID = value
					invalid.DataObject = &copy
				} else {
					copy := *fact.StateAuthority
					copy.AuthoritativeResourceID = value
					invalid.StateAuthority = &copy
				}
				if err := parse(invalid); err == nil {
					t.Fatal("source identity lost its scalar bound")
				}
			}
			invalid := fact
			invalid.OwnerDomainID = "unknown"
			if err := parse(invalid); err == nil {
				t.Fatal("catalog retirement bypassed Domain ownership")
			}
			invalid = fact
			invalid.Standing = "law"
			if err := parse(invalid); err == nil {
				t.Fatal("catalog retirement bypassed law standing")
			}
		})
	}
}

func TestCatalogRetirementSnapshotSafety(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	s, err := Open(ctx, filepath.Join(root, "source.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	snapshot := filepath.Join(root, "snapshot.db")
	manifest, err := Backup(ctx, s, snapshot)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := VerifyBackup(ctx, snapshot, manifest.SchemaVersion); err != nil {
		t.Fatal(err)
	}
	destination := filepath.Join(root, "restored.db")
	if _, err := RestoreBackup(ctx, snapshot, destination); err != nil {
		t.Fatal(err)
	}
	checksum, err := fileSHA256(snapshot)
	if err != nil || checksum != manifest.DBChecksum {
		t.Fatalf("restore changed source snapshot: checksum=%s, error=%v", checksum, err)
	}
	if _, err := RestoreBackup(ctx, snapshot, destination); err == nil {
		t.Fatal("restore overwrote an existing destination")
	}
}
