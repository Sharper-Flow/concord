package main

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestHostRegistryQualification(t *testing.T) {
	dir := os.Getenv("CONCORD_QUALIFY_REGISTRY_DIR")
	if dir == "" {
		t.Skip("CONCORD_QUALIFY_REGISTRY_DIR is not set")
	}
	data, err := os.ReadFile("../../contracts/agent-primary-profiles.v1.json")
	if err != nil {
		t.Fatal(err)
	}
	var manifest struct {
		Profiles []struct {
			ID   string `json:"id"`
			Mode string `json:"mode"`
		} `json:"profiles"`
	}
	if err := json.Unmarshal(data, &manifest); err != nil {
		t.Fatal(err)
	}
	for _, variant := range []struct {
		file, observed string
	}{
		{"full.json", ""},
		{"missing.json", "not registered (missing or disabled)"},
		{"wrong-mode.json", "registered as a subagent"},
	} {
		t.Run(variant.file, func(t *testing.T) {
			registry, err := os.ReadFile(filepath.Join(dir, variant.file))
			if err != nil {
				t.Fatal(err)
			}
			host := hostCommandResolution{Command: defaultHostCommand, Registry: registry}
			found := false
			for _, profile := range manifest.Profiles {
				if profile.Mode != "" && profile.Mode != "primary" && profile.Mode != "all" {
					continue
				}
				if profile.ID == "Concord" {
					found = true
				}
				err := verifyHostRegistersHandle(host, profile.ID)
				if profile.ID == "Concord" && variant.observed != "" {
					var refusal *hostRegistrationError
					if !errors.As(err, &refusal) || refusal.Handle != profile.ID || refusal.Observed != variant.observed {
						t.Errorf("%s: got %v, want refusal %q", profile.ID, err, variant.observed)
					}
				} else if err != nil {
					t.Errorf("%s: %v", profile.ID, err)
				}
			}
			if !found {
				t.Error("manifest does not declare Concord as a primary")
			}
		})
	}
}
