package store

import (
	"archive/tar"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func setupShapeRequest(t *testing.T, product string) DomainShapeSetupRequest {
	t.Helper()
	return DomainShapeSetupRequest{RepoPath: t.TempDir(), ProductKey: product, ProjectKey: product + "-home"}
}

func writeSetupInput(t *testing.T, request DomainShapeSetupRequest, name, data string) {
	t.Helper()
	target := filepath.Join(request.RepoPath, filepath.FromSlash(name))
	if err := os.MkdirAll(filepath.Dir(target), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(target, []byte(data), 0644); err != nil {
		t.Fatal(err)
	}
}

func setupShapeSnapshot(t *testing.T, repo string) map[string]string {
	t.Helper()
	result := map[string]string{}
	err := filepath.WalkDir(repo, func(name string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		relative, _ := filepath.Rel(repo, name)
		info, err := entry.Info()
		if err != nil {
			return err
		}
		value := fmt.Sprintf("%v %v", info.Mode(), info.ModTime().UnixNano())
		if entry.Type().IsRegular() {
			data, err := os.ReadFile(name)
			if err != nil {
				return err
			}
			value += string(data)
		}
		result[filepath.ToSlash(relative)] = value
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return result
}

func TestDomainShapeSetupIndependentProductsAndRepeat(t *testing.T) {
	var registries []KnowledgeDomainRegistry
	for _, key := range []string{"orchard", "harbor"} {
		t.Run(key, func(t *testing.T) {
			request := setupShapeRequest(t, key)
			before := setupShapeSnapshot(t, request.RepoPath)
			plan, err := PrepareDomainShapeSetup(request)
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(before, setupShapeSnapshot(t, request.RepoPath)) {
				t.Fatal("prepare changed the filesystem")
			}
			result, err := plan.Apply()
			if err != nil || len(result.Created) != 10 || len(result.Updated) != 0 {
				t.Fatalf("apply = %+v, %v", result, err)
			}
			// Exercise the normal Git-derived reader without creating a commit.
			runner := setupShapeGitRunner(t, request.RepoPath)
			manifest, missing, err := readKnowledgeManifestRunner(context.Background(), runner, request.RepoPath, strings.Repeat("a", 40), manifestSharedHomeRole)
			if err != nil || missing {
				t.Fatalf("normal read: missing=%v err=%v", missing, err)
			}
			registries = append(registries, manifest.DomainRegistry)
			if manifest.DomainRegistry.ProductKey != key || manifest.DomainRegistry.RootDomainID != "product-root:"+key || len(manifest.Records) != 0 || len(manifest.DomainRegistry.Domains) != 1 || manifest.DocContract != nil {
				t.Fatalf("setup seeded Product-specific corpus: %+v", manifest)
			}
			data, err := os.ReadFile(filepath.Join(request.RepoPath, knowledgePackageMapPath))
			if err != nil {
				t.Fatal(err)
			}
			packages, err := parseKnowledgePackageMap(data, manifest.DomainRegistry)
			if err != nil || packages.SchemaVersion != "1.1" || len(packages.Packages) != 0 {
				t.Fatalf("package map: %+v, %v", packages, err)
			}
			toolBytes, _ := os.ReadFile(filepath.Join(request.RepoPath, projectToolingManifestPath))
			tools, err := ParseProjectToolingManifest(toolBytes)
			if err != nil || len(tools.Declared) != 0 || tools.DomainShape == nil || ProjectToolingInvocationDeclared(tools, []string{"true"}) {
				t.Fatalf("initial tooling must grant no check proof: %+v, %v", tools, err)
			}
			before = setupShapeSnapshot(t, request.RepoPath)
			repeated, err := PrepareDomainShapeSetup(request)
			if err != nil {
				t.Fatal(err)
			}
			result, err = repeated.Apply()
			if err != nil || len(result.Created) != 0 || len(result.Updated) != 0 || !reflect.DeepEqual(before, setupShapeSnapshot(t, request.RepoPath)) {
				t.Fatalf("repeat changed bytes, modes or mtimes: %+v, %v", result, err)
			}
		})
	}
	if registries[0].RootDomainID == registries[1].RootDomainID {
		t.Fatal("Products share a root identity")
	}
}

func TestDomainShapeSetupPreservesAuthoredIdentitiesAndTooling(t *testing.T) {
	request := setupShapeRequest(t, "orchard")
	registry := `{"schema_version":"1.0","product_key":"orchard","root_domain_id":"product-root:orchard","domains":[{"domain_id":"product-root:orchard","name":"Orchard","purpose":"Shared behavior.","status":"current","architecture_relations":[]},{"domain_id":"fruit-care","name":"Care","purpose":"Care for fruit.","parent_domain_id":"product-root:orchard","status":"current","architecture_relations":[]}]}`
	tooling := `{"schema_version":"1.0","project":"orchard-home","tools":[{"id":"unit-check","purpose":"Check units.","invocation":"go test ./...","tier":"fast"}],"domain_shape":{"boundary_check":"unit-check","required_checks":["unit-check"]}}` + "\n"
	writeSetupInput(t, request, knowledgeRegistryPath, registry)
	writeSetupInput(t, request, projectToolingManifestPath, tooling)
	writeSetupInput(t, request, knowledgePackageMapPath, `{"schema_version":"1.0","packages":[{"path":"fruit","domain_ids":["fruit-care"]}]}`)
	if err := os.Mkdir(filepath.Join(request.RepoPath, "fruit"), 0755); err != nil {
		t.Fatal(err)
	}
	plan, err := PrepareDomainShapeSetup(request)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := plan.Apply(); err != nil {
		t.Fatal(err)
	}
	for name, expected := range map[string]string{knowledgeRegistryPath: registry, projectToolingManifestPath: tooling} {
		actual, _ := os.ReadFile(filepath.Join(request.RepoPath, name))
		if string(actual) != expected {
			t.Fatalf("rewrote authored %s", name)
		}
	}
	before := setupShapeSnapshot(t, request.RepoPath)
	request.ProductKey = "harbor"
	if _, err := PrepareDomainShapeSetup(request); err == nil || !reflect.DeepEqual(before, setupShapeSnapshot(t, request.RepoPath)) {
		t.Fatal("cross-Product setup must refuse without mutation")
	}
}

func TestDomainShapeSetupAddsOnlyMissingToolingDeclaration(t *testing.T) {
	request := setupShapeRequest(t, "orchard")
	tooling := "{\n  \"schema_version\":\"1.0\",\"project\":\"orchard-home\",\"tools\":[{\"id\":\"unit-check\",\"purpose\":\"Check units.\",\"invocation\":\"go test ./...\",\"tier\":\"fast\"}]\n}\n"
	writeSetupInput(t, request, projectToolingManifestPath, tooling)
	plan, err := PrepareDomainShapeSetup(request)
	if err != nil {
		t.Fatal(err)
	}
	result, err := plan.Apply()
	if err != nil || !reflect.DeepEqual(result.Updated, []string{projectToolingManifestPath}) {
		t.Fatalf("apply = %+v, %v", result, err)
	}
	data, _ := os.ReadFile(filepath.Join(request.RepoPath, projectToolingManifestPath))
	if !bytes.HasPrefix(data, []byte(tooling[:len(tooling)-2])) {
		t.Fatalf("rewrote existing tooling bytes: %s", data)
	}
	if _, err := ParseProjectToolingManifest(data); err != nil {
		t.Fatal(err)
	}
}

func TestDomainShapeSetupPreservesCoverageMetadata(t *testing.T) {
	var relations []any
	for index := range 64 {
		anchorKind := []string{"go_test", "scenario", "validator", "generated", "adapter_test", "go_package"}[index%6]
		ownerKind := []string{"go_package", "go_test", "adapter_test", "validator"}[index%4]
		value := fmt.Sprintf("synthetic-%d", index)
		if index == 0 {
			value = strings.Repeat("果", 512)
		} else if index == 1 {
			value = "果実園"
		}
		relations = append(relations, map[string]any{
			"anchor": map[string]string{"kind": anchorKind, "value": value},
			"owner":  map[string]string{"kind": ownerKind, "value": value},
		})
	}
	coverage, err := json.MarshalIndent(relations, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	for _, declared := range []bool{false, true} {
		t.Run(fmt.Sprintf("domain-shape-%v", declared), func(t *testing.T) {
			request := setupShapeRequest(t, "orchard")
			tooling := "{\n  \"schema_version\":\"1.0\",\"project\":\"orchard-home\",\"tools\":[{\"id\":\"unit-check\",\"purpose\":\"Check units.\",\"invocation\":\"go test ./...\",\"tier\":\"fast\",\"coverage\":" + string(coverage) + "}]"
			if declared {
				tooling += ",\"domain_shape\":{}"
			}
			tooling += "\n}\n\t"
			writeSetupInput(t, request, projectToolingManifestPath, tooling)
			before := setupShapeSnapshot(t, request.RepoPath)
			plan, err := PrepareDomainShapeSetup(request)
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(before, setupShapeSnapshot(t, request.RepoPath)) {
				t.Fatal("prepare changed authored metadata")
			}
			result, err := plan.Apply()
			if err != nil {
				t.Fatal(err)
			}
			expected := tooling
			if !declared {
				trimmed := strings.TrimRight(tooling, " \t\r\n")
				expected = trimmed[:len(trimmed)-1] + ",\n  \"domain_shape\": {}\n}" + tooling[len(trimmed):]
			}
			actual, err := os.ReadFile(filepath.Join(request.RepoPath, projectToolingManifestPath))
			if err != nil || string(actual) != expected {
				t.Fatalf("authored tooling bytes changed: %v", err)
			}
			if declared && len(result.Updated) != 0 || !declared && !reflect.DeepEqual(result.Updated, []string{projectToolingManifestPath}) {
				t.Fatalf("unexpected tooling updates: %+v", result)
			}
		})
	}
}

func TestDomainShapeSetupRefusesMalformedCoverageMetadata(t *testing.T) {
	anchor := `{"kind":"scenario","value":"corpus:example"}`
	owner := `{"kind":"go_test","value":"store_test.go:TestExample"}`
	relation := `{"anchor":` + anchor + `,"owner":` + owner + `}`
	cases := []struct{ name, coverage string }{
		{"null", `null`},
		{"object", `{}`},
		{"empty", `[]`},
		{"too-many", `[` + strings.Repeat(relation+`,`, 64) + relation + `]`},
		{"duplicate", `[` + relation + `,` + relation + `]`},
		{"null-relation", `[null]`},
		{"array-relation", `[[]]`},
		{"missing-anchor", `[{"owner":` + owner + `}]`},
		{"missing-owner", `[{"anchor":` + anchor + `}]`},
		{"unknown-relation-field", `[{"anchor":` + anchor + `,"owner":` + owner + `,"extra":true}]`},
		{"duplicate-key", `[{"anchor":` + anchor + `,"anchor":` + anchor + `,"owner":` + owner + `}]`},
	}
	for _, endpoint := range []string{"anchor", "owner"} {
		for _, bad := range []struct{ name, value string }{
			{"null", `null`},
			{"array", `[]`},
			{"missing-kind", `{"value":"example"}`},
			{"missing-value", `{"kind":"go_test"}`},
			{"unknown-field", `{"kind":"go_test","value":"example","extra":true}`},
			{"noncanonical-field", `{"Kind":"go_test","value":"example"}`},
			{"null-kind", `{"kind":null,"value":"example"}`},
			{"unknown-kind", `{"kind":"recipe","value":"example"}`},
			{"wrong-kind-type", `{"kind":1,"value":"example"}`},
			{"null-value", `{"kind":"go_test","value":null}`},
			{"wrong-value-type", `{"kind":"go_test","value":1}`},
			{"short-value", `{"kind":"go_test","value":"ab"}`},
			{"long-value", `{"kind":"go_test","value":"` + strings.Repeat("果", 513) + `"}`},
			{"leading-space", `{"kind":"go_test","value":" example"}`},
			{"trailing-space", `{"kind":"go_test","value":"example\n"}`},
			{"blank", `{"kind":"go_test","value":"   "}`},
		} {
			entry := `{"anchor":` + anchor + `,"owner":` + bad.value + `}`
			if endpoint == "anchor" {
				entry = `{"anchor":` + bad.value + `,"owner":` + owner + `}`
			}
			cases = append(cases, struct{ name, coverage string }{endpoint + "-" + bad.name, `[` + entry + `]`})
		}
	}
	for _, kind := range []string{"scenario", "generated"} {
		cases = append(cases, struct{ name, coverage string }{"owner-" + kind, `[{"anchor":` + anchor + `,"owner":{"kind":"` + kind + `","value":"example"}}]`})
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			request := setupShapeRequest(t, "orchard")
			tooling := `{"schema_version":"1.0","project":"orchard-home","tools":[{"id":"unit-check","purpose":"Check units.","invocation":"go test ./...","tier":"fast","coverage":` + test.coverage + `}],"domain_shape":{}}`
			writeSetupInput(t, request, projectToolingManifestPath, tooling)
			before := setupShapeSnapshot(t, request.RepoPath)
			if _, err := PrepareDomainShapeSetup(request); err == nil {
				t.Fatal("accepted malformed coverage metadata")
			}
			if !reflect.DeepEqual(before, setupShapeSnapshot(t, request.RepoPath)) {
				t.Fatal("failed preparation changed filesystem")
			}
		})
	}
}

func TestDomainShapeSetupRefusesBeforeWriting(t *testing.T) {
	for _, test := range []struct{ name, path, data string }{
		{"registry", knowledgeRegistryPath, `{}`},
		{"duplicate", knowledgeRegistryPath, `{"schema_version":"1.0","schema_version":"1.0"}`},
		{"head", knowledgeHeadPath, `{}`},
		{"map", knowledgePackageMapPath, `{"schema_version":"1.1","packages":[{"path":"fruit","role":"domain","owner_domain_id":"foreign"}]}`},
		{"ratchet", domainShapeRatchetPath, `{"schema_version":"1.0","mixed_roots":["missing"],"imports":[]}`},
		{"ratchet-unknown", domainShapeRatchetPath, `{"schema_version":"1.0","mixed_roots":[],"imports":[],"extra":true}`},
		{"tooling", projectToolingManifestPath, `{"schema_version":"1.0","project":"other","tools":[],"domain_shape":{}}`},
		{"shape", projectToolingManifestPath, `{"schema_version":"1.0","project":"orchard-home","tools":[],"domain_shape":{"boundary_check":"unknown"}}`},
		{"tool-metadata", projectToolingManifestPath, `{"schema_version":"1.0","project":"orchard-home","tools":[{"id":"unit-check","invocation":"go test ./..."}]}`},
		{"tool-unknown", projectToolingManifestPath, `{"schema_version":"1.0","project":"orchard-home","tools":[{"id":"unit-check","invocation":"go test ./...","purpose":"Check units.","tier":"fast","extra":true}]}`},
		{"null-shape", projectToolingManifestPath, `{"schema_version":"1.0","project":"orchard-home","tools":[],"domain_shape":null}`},
		{"fact", domainShapeFactsDir + "/bad.json", `{"schema_version":"1.0","id":"bad","unknown":true}`},
		{"legacy", "docs/knowledge/manifest.json", `{}`},
		{"trailing", projectToolingManifestPath, `{} {}`},
	} {
		t.Run(test.name, func(t *testing.T) {
			request := setupShapeRequest(t, "orchard")
			writeSetupInput(t, request, test.path, test.data)
			before := setupShapeSnapshot(t, request.RepoPath)
			if _, err := PrepareDomainShapeSetup(request); err == nil {
				t.Fatal("accepted malformed/conflicting input")
			}
			if !reflect.DeepEqual(before, setupShapeSnapshot(t, request.RepoPath)) {
				t.Fatal("failed preparation changed filesystem")
			}
		})
	}
}

func TestDomainShapeSetupRefusesSymlinksAndPreparedDrift(t *testing.T) {
	for _, name := range []string{".concord", knowledgeRegistryPath, projectToolingManifestPath, domainShapeFactsDir + "/escape"} {
		t.Run(name, func(t *testing.T) {
			request := setupShapeRequest(t, "orchard")
			outside := t.TempDir()
			target := filepath.Join(request.RepoPath, name)
			if err := os.MkdirAll(filepath.Dir(target), 0755); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink(outside, target); err != nil {
				t.Fatal(err)
			}
			before := setupShapeSnapshot(t, request.RepoPath)
			if _, err := PrepareDomainShapeSetup(request); err == nil || !reflect.DeepEqual(before, setupShapeSnapshot(t, request.RepoPath)) {
				t.Fatal("symlink setup did not refuse unchanged")
			}
			entries, _ := os.ReadDir(outside)
			if len(entries) != 0 {
				t.Fatal("wrote outside the home")
			}
		})
	}
	request := setupShapeRequest(t, "orchard")
	plan, err := PrepareDomainShapeSetup(request)
	if err != nil {
		t.Fatal(err)
	}
	writeSetupInput(t, request, projectToolingManifestPath, `{"schema_version":"1.0","project":"orchard-home","tools":[],"domain_shape":{}}`)
	before := setupShapeSnapshot(t, request.RepoPath)
	result, err := plan.Apply()
	if err == nil || len(result.Created) != 0 || !reflect.DeepEqual(before, setupShapeSnapshot(t, request.RepoPath)) {
		t.Fatalf("stale preparation wrote files: %+v, %v", result, err)
	}
}

func TestDomainShapeSetupPublicationFailureIsNonDestructive(t *testing.T) {
	request := setupShapeRequest(t, "orchard")
	writeSetupInput(t, request, "existing.json", "operator bytes")
	root, err := os.OpenRoot(request.RepoPath)
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	if published, err := publishDomainShapeFile(root, "existing.json", []byte("new"), 0644, false); err == nil || published {
		t.Fatal("exclusive publication replaced an existing file")
	}
	data, _ := os.ReadFile(filepath.Join(request.RepoPath, "existing.json"))
	if string(data) != "operator bytes" {
		t.Fatal("failed publication changed existing bytes")
	}
	entries, _ := os.ReadDir(request.RepoPath)
	if len(entries) != 1 {
		t.Fatal("failed publication leaked staging files")
	}
}

type domainShapeSetupGit struct{ archive []byte }

func setupShapeGitRunner(t *testing.T, repo string) domainShapeSetupGit {
	t.Helper()
	var buffer bytes.Buffer
	writer := tar.NewWriter(&buffer)
	err := filepath.WalkDir(filepath.Join(repo, knowledgeShardRoot), func(name string, entry fs.DirEntry, err error) error {
		if err != nil || entry.IsDir() {
			return err
		}
		data, err := os.ReadFile(name)
		if err != nil {
			return err
		}
		relative, _ := filepath.Rel(repo, name)
		if err := writer.WriteHeader(&tar.Header{Name: filepath.ToSlash(relative), Mode: 0644, Size: int64(len(data)), Typeflag: tar.TypeReg}); err != nil {
			return err
		}
		_, err = writer.Write(data)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	return domainShapeSetupGit{archive: buffer.Bytes()}
}

func (runner domainShapeSetupGit) Run(_ context.Context, _ string, args ...string) ([]byte, error) {
	switch args[0] {
	case "ls-tree":
		return []byte("100644 blob " + strings.Repeat("a", 40) + "\t" + knowledgeHeadPath + "\x00"), nil
	case "archive":
		return runner.archive, nil
	default:
		return nil, fmt.Errorf("unexpected git command: %v", args)
	}
}

// Ensure the initial baseline uses the check protocol's one ratchet model.
func TestDomainShapeSetupRatchetModel(t *testing.T) {
	request := setupShapeRequest(t, "orchard")
	plan, err := PrepareDomainShapeSetup(request)
	if err != nil {
		t.Fatal(err)
	}
	var ratchet DomainShapeRatchet
	if err := json.Unmarshal(plan.writes[domainShapeRatchetPath], &ratchet); err != nil || ratchet.SchemaVersion != "1.0" || ratchet.MixedRoots == nil || ratchet.Imports == nil || len(ratchet.MixedRoots)+len(ratchet.Imports) != 0 {
		t.Fatalf("baseline = %+v, %v", ratchet, err)
	}
}

func TestDomainShapeSetupSeedsDeclaredMixedRoots(t *testing.T) {
	for _, version := range []string{"1.0", "1.1"} {
		t.Run(version, func(t *testing.T) {
			request := setupShapeRequest(t, "orchard")
			plan, err := PrepareDomainShapeSetup(request)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := plan.Apply(); err != nil {
				t.Fatal(err)
			}
			if err := os.Remove(filepath.Join(request.RepoPath, domainShapeRatchetPath)); err != nil {
				t.Fatal(err)
			}
			entry := `{"path":"fruit","domain_ids":["product-root:orchard"]}`
			if version == "1.1" {
				entry = `{"path":"fruit","role":"mixed","domain_ids":["product-root:orchard","fruit-care"]}`
				writeSetupInput(t, request, knowledgeRegistryPath, `{"schema_version":"1.0","product_key":"orchard","root_domain_id":"product-root:orchard","domains":[{"domain_id":"product-root:orchard","name":"Orchard","purpose":"Shared behavior.","status":"current","architecture_relations":[]},{"domain_id":"fruit-care","name":"Care","purpose":"Care for fruit.","parent_domain_id":"product-root:orchard","status":"current","architecture_relations":[]}]}`)
			}
			writeSetupInput(t, request, knowledgePackageMapPath, fmt.Sprintf(`{"schema_version":%q,"packages":[%s]}`, version, entry))
			if err := os.Mkdir(filepath.Join(request.RepoPath, "fruit"), 0755); err != nil {
				t.Fatal(err)
			}
			plan, err = PrepareDomainShapeSetup(request)
			if err != nil {
				t.Fatal(err)
			}
			var ratchet DomainShapeRatchet
			if err := json.Unmarshal(plan.writes[domainShapeRatchetPath], &ratchet); err != nil || !reflect.DeepEqual(ratchet.MixedRoots, []string{"fruit"}) || len(ratchet.Imports) != 0 {
				t.Fatalf("existing mixed roots were not seeded: %+v %v", ratchet, err)
			}
		})
	}
}

func TestDomainShapeSetupPreservesDeclaredRootsFactsAndBaseline(t *testing.T) {
	request := setupShapeRequest(t, "orchard")
	registry := `{"schema_version":"1.0","product_key":"orchard","root_domain_id":"product-root:orchard","domains":[{"domain_id":"product-root:orchard","name":"Orchard","purpose":"Shared behavior.","status":"current","architecture_relations":[]},{"domain_id":"fruit-care","name":"Care","purpose":"Care for fruit.","parent_domain_id":"product-root:orchard","status":"current","architecture_relations":[]}]}`
	roots := `{"schema_version":"1.1","packages":[{"path":"fruit","role":"mixed","domain_ids":["product-root:orchard","fruit-care"]},{"path":"packing","role":"composition"}]}`
	baseline := `{"schema_version":"1.0","mixed_roots":["fruit"],"imports":[{"source_root":"fruit","target_root":"packing"}]}`
	fact := `{"schema_version":"1.0","id":"setting","kind":"config_coupling","owner_domain_id":"fruit-care","owning_root":"fruit","standing":"observed","config_coupling":{"setting":"WATER","source":"environment","read_by":{"project_id":"orchard-home","path":"fruit/config.go","symbol":"ReadWater"},"effect":"Controls watering.","environments":["test"]}}`
	inputs := map[string]string{knowledgeRegistryPath: registry, knowledgePackageMapPath: roots, domainShapeRatchetPath: baseline, domainShapeFactsDir + "/setting.json": fact}
	for name, data := range inputs {
		writeSetupInput(t, request, name, data)
	}
	for _, dir := range []string{"fruit", "packing"} {
		if err := os.Mkdir(filepath.Join(request.RepoPath, dir), 0755); err != nil {
			t.Fatal(err)
		}
	}
	plan, err := PrepareDomainShapeSetup(request)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := plan.Apply(); err != nil {
		t.Fatal(err)
	}
	for name, expected := range inputs {
		actual, err := os.ReadFile(filepath.Join(request.RepoPath, name))
		if err != nil || string(actual) != expected {
			t.Fatalf("rewrote existing %s: %v", name, err)
		}
	}
}

func TestDomainShapeSetupRefusesRootSymlinkAndOversize(t *testing.T) {
	request := setupShapeRequest(t, "orchard")
	link := filepath.Join(t.TempDir(), "home")
	if err := os.Symlink(request.RepoPath, link); err != nil {
		t.Fatal(err)
	}
	linked := request
	linked.RepoPath = link
	if _, err := PrepareDomainShapeSetup(linked); err == nil {
		t.Fatal("accepted symlink home")
	}
	writeSetupInput(t, request, knowledgeRegistryPath, strings.Repeat(" ", maxKnowledgeRecord+1))
	if _, err := PrepareDomainShapeSetup(request); err == nil {
		t.Fatal("accepted oversized setup input")
	}
}

func TestDomainShapeSetupPreservesLargeValidTooling(t *testing.T) {
	request := setupShapeRequest(t, "orchard")
	tools := []map[string]string{}
	for index := range 64 {
		tools = append(tools, map[string]string{"id": fmt.Sprintf("check-%d", index), "purpose": strings.Repeat("x", 256), "invocation": "go test ./...", "tier": "fast"})
	}
	data, err := json.Marshal(map[string]any{"schema_version": "1.0", "project": request.ProjectKey, "tools": tools, "domain_shape": map[string]any{}})
	if err != nil || len(data) <= maxKnowledgeRecord {
		t.Fatalf("fixture must exceed the knowledge shard cap: %d, %v", len(data), err)
	}
	writeSetupInput(t, request, projectToolingManifestPath, string(data))
	plan, err := PrepareDomainShapeSetup(request)
	if err != nil {
		t.Fatal(err)
	}
	result, err := plan.Apply()
	if err != nil || len(result.Updated) != 0 {
		t.Fatalf("large tooling = %+v, %v", result, err)
	}
	actual, _ := os.ReadFile(filepath.Join(request.RepoPath, projectToolingManifestPath))
	if !bytes.Equal(data, actual) {
		t.Fatal("rewrote valid tooling")
	}
}
