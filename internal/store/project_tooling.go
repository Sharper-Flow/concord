package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"regexp"
	"slices"
	"strings"
)

// projectToolingManifestPath is the per-project record of declared checks the
// refine-exit proof guard reads (CD-0192). The guard compares the bound
// worktree-verify run's argv against the manifest the Project declares on its
// default ref, never the working tree: a change branch must not declare its
// own passing check.
const projectToolingManifestPath = ".concord/tooling.v1.json"

// projectToolingRefusalBudget bounds the declared-check list one refusal
// renders, so a 64-tool manifest cannot push a failure message past the
// envelope bounds.
const projectToolingRefusalBudget = 900

// ProjectDeclaredTool is one declared check: the tool's stable id and the
// invocation the default ref declares, kept verbatim so a refusal can name
// exactly what the Project declared.
type ProjectDeclaredTool struct {
	ID         string
	Invocation string
	Coverage   []ProjectToolCoverage
}

type ProjectToolCoverage struct {
	Anchor     FootprintCheck `json:"anchor"`
	RecipePath string         `json:"recipe_path"`
	CaseID     string         `json:"case_id"`
}

// ProjectResolvedCoverage binds a declaration to one immutable recipe case.
// Files contains the exact pinned harness and fixture inputs, not execution proof.
type ProjectResolvedCoverage struct {
	Anchor       FootprintCheck
	RecipeSource WorkContextReadingSource
	RecipeDigest string
	CaseID       string
	Recipe       NativeRecipeCase
	RecipeRaw    []byte
	Files        []NativeOracleFile
}

// ProjectToolingManifest is the resolved tooling declaration the refine-exit
// proof guard compares the bound verify run against.
type ProjectToolingManifest struct {
	Project          string
	Declared         []ProjectDeclaredTool
	DomainShape      *DomainShapeTooling
	SourceCommit     string
	SourceProjectID  string
	ManifestDigest   string
	ResolvedCoverage []ProjectResolvedCoverage
}

// DomainShapeTooling references existing tool IDs; it grants no execution authority.
// RequiredChecks declares membership, not successful execution of those checks.
type DomainShapeTooling struct {
	BoundaryCheck       string   `json:"boundary_check,omitempty"`
	DataObjectExtractor string   `json:"data_object_extractor,omitempty"`
	ConfigExtractor     string   `json:"config_extractor,omitempty"`
	ReferenceCheck      string   `json:"reference_check,omitempty"`
	RequiredChecks      []string `json:"required_checks,omitempty"`
}

var projectToolID = regexp.MustCompile(`^[a-z][a-z0-9-]{1,63}$`)
var projectToolPath = regexp.MustCompile(`^[A-Za-z0-9._-]+(/[A-Za-z0-9._-]+)*$`)
var projectToolPositiveInteger = regexp.MustCompile(`^[1-9][0-9]{0,5}$`)
var projectToolDuration = regexp.MustCompile(`^[1-9][0-9]{0,5}(ms|s|m|h)$`)

func projectToolingPathValid(value string) bool {
	return len(value) >= 3 && len(value) <= 512 && projectToolPath.MatchString(value) && validateWorkContextRepoPath(value) == nil
}

func projectToolingAnchorValid(anchor FootprintCheck) bool {
	if (!coverageAnchorKinds[anchor.Kind] && anchor.Kind != "go_package") || len(anchor.Value) == 0 || len(anchor.Value) > 512 || strings.TrimSpace(anchor.Value) != anchor.Value {
		return false
	}
	return !strings.ContainsFunc(anchor.Value, func(r rune) bool { return r < 32 || r == 127 })
}

// projectToolingDirectCommand recognizes only commands with no selection-changing
// flags. Wrappers remain legal refine declarations but convey no coverage.
func projectToolingDirectCommand(invocation string) (string, []string, bool) {
	if !projectToolingPlainInvocation(invocation) {
		return "", nil, false
	}
	args := strings.Fields(invocation)
	if len(args) == 2 && args[0] == "python3" && strings.HasPrefix(args[1], "scripts/") && strings.HasSuffix(args[1], ".py") && projectToolingPathValid(args[1]) {
		return "python3", args[1:], true
	}
	if len(args) < 3 || args[1] != "test" || (args[0] != "go" && args[0] != "bun") {
		return "", nil, false
	}
	var inputs []string
	seenFlags := map[string]bool{}
	for i := 2; i < len(args); i++ {
		arg := args[i]
		if strings.HasPrefix(arg, "-") {
			flag, value, hasValue := strings.Cut(arg, "=")
			if seenFlags[flag] {
				return "", nil, false
			}
			seenFlags[flag] = true
			if args[0] == "go" {
				switch flag {
				case "-race", "-v", "-json":
					if hasValue {
						return "", nil, false
					}
				case "-count", "-timeout", "-p", "-parallel":
					if !hasValue {
						i++
						if i >= len(args) {
							return "", nil, false
						}
						value = args[i]
					}
					if flag == "-count" && value != "1" || flag == "-timeout" && !projectToolDuration.MatchString(value) || (flag == "-p" || flag == "-parallel") && !projectToolPositiveInteger.MatchString(value) {
						return "", nil, false
					}
				default:
					return "", nil, false
				}
			} else {
				if flag != "--timeout" {
					return "", nil, false
				}
				if !hasValue {
					i++
					if i >= len(args) {
						return "", nil, false
					}
					value = args[i]
				}
				if !projectToolPositiveInteger.MatchString(value) {
					return "", nil, false
				}
			}
			continue
		}
		if args[0] == "go" {
			base := strings.TrimSuffix(strings.TrimPrefix(arg, "./"), "/...")
			if arg != "." && arg != "./..." && (!strings.HasPrefix(arg, "./") || validateWorkContextRepoPath(base) != nil || !projectToolPath.MatchString(base)) {
				return "", nil, false
			}
		} else if !projectToolingPathValid(strings.TrimPrefix(arg, "./")) || !strings.HasSuffix(arg, ".test.ts") {
			return "", nil, false
		}
		if slices.Contains(inputs, arg) {
			return "", nil, false
		}
		inputs = append(inputs, arg)
	}
	return args[0], inputs, len(inputs) > 0
}

// ProjectToolingCoverageLineage checks command-to-recipe input membership only.
// The approved anchor/case relation and native receipt still need qualification.
func ProjectToolingCoverageLineage(invocation string, recipe NativeRecipeCase) bool {
	kind, inputs, ok := projectToolingDirectCommand(invocation)
	if !ok {
		return false
	}
	if kind == "go" {
		if recipe.Kind != "go_top_level_v1" && recipe.Kind != "go_package_v1" {
			return false
		}
		for _, input := range inputs {
			if input == "./..." || input == "." && recipe.Cwd == "." || input == "./"+recipe.Cwd {
				return true
			}
			if strings.HasSuffix(input, "/...") {
				root := strings.TrimSuffix(strings.TrimPrefix(input, "./"), "/...")
				if recipe.Cwd == root || strings.HasPrefix(recipe.Cwd, root+"/") {
					return true
				}
			}
		}
		return false
	}
	if recipe.Kind != "target_runner_v1" || recipe.runner != "check" {
		return false
	}
	for _, input := range inputs {
		found := false
		for _, check := range recipe.checks {
			if kind == "python3" && slices.Equal(check.Argv, []string{"python3", projectToolingRelativePath(recipe.Cwd, input)}) {
				found = slices.Contains(recipe.HarnessFiles, input)
			}
			if kind == "bun" {
				files, _, ok := projectToolingAdapterCommand(recipe, check)
				found = found || ok && slices.Contains(files, strings.TrimPrefix(input, "./"))
			}
		}
		if !found {
			return false
		}
	}
	return true
}

func validateDomainShapeTooling(shape *DomainShapeTooling, tools []ProjectDeclaredTool) error {
	if shape == nil {
		return nil
	}
	declared := map[string]bool{}
	for _, tool := range tools {
		declared[tool.ID] = true
	}
	for _, id := range []string{shape.BoundaryCheck, shape.DataObjectExtractor, shape.ConfigExtractor, shape.ReferenceCheck} {
		if id != "" && !declared[id] {
			return fmt.Errorf("domain_shape references undeclared tool %q", id)
		}
	}
	if len(shape.RequiredChecks) > 64 {
		return fmt.Errorf("domain_shape required_checks exceeds 64 entries")
	}
	seen := map[string]bool{}
	for _, id := range shape.RequiredChecks {
		if !declared[id] || seen[id] {
			return fmt.Errorf("domain_shape required_checks must name distinct declared tools: %q", id)
		}
		seen[id] = true
	}
	return nil
}

// ParseProjectToolingManifest reads the declared checks out of a
// .concord/tooling.v1.json document. It admits only a current manifest whose
// tools each carry a splittable invocation, because the guard's argv equality
// is exact: an invocation that splits to nothing declares no argv at all.
func ParseProjectToolingManifest(raw []byte) (*ProjectToolingManifest, error) {
	var document struct {
		SchemaVersion string          `json:"schema_version"`
		Project       string          `json:"project"`
		DomainShape   json.RawMessage `json:"domain_shape"`
		Tools         []struct {
			ID             string          `json:"id"`
			Invocation     string          `json:"invocation"`
			Purpose        string          `json:"purpose"`
			Tier           string          `json:"tier"`
			CostHint       string          `json:"cost_hint"`
			ConfigPath     string          `json:"config_path"`
			AutomationPath string          `json:"automation_path"`
			Notes          string          `json:"notes"`
			Coverage       json.RawMessage `json:"coverage"`
		} `json:"tools"`
	}
	if len(raw) > 65536 {
		return nil, fmt.Errorf("tooling manifest exceeds 65536 bytes")
	}
	if err := rejectDuplicateJSONKeys(raw); err != nil {
		return nil, err
	}
	if err := decodeStrictKnowledgeJSON(raw, &document); err != nil {
		return nil, newFailure(KindInvalidPayload, "project_tooling", "tooling manifest is not valid JSON: "+err.Error(), false, "repair "+projectToolingManifestPath+" on the default ref")
	}
	if document.SchemaVersion != "1.0" {
		return nil, newFailure(KindSchemaUnsupported, "project_tooling", fmt.Sprintf("tooling manifest schema_version must be \"1.0\", got %q", document.SchemaVersion), false, "declare schema_version \"1.0\" in "+projectToolingManifestPath)
	}
	if !projectToolID.MatchString(document.Project) {
		return nil, newFailure(KindInvalidPayload, "project_tooling", "tooling manifest names no project", false, "declare the project identifier in "+projectToolingManifestPath)
	}
	if document.Tools == nil || len(document.Tools) > 64 || (len(document.Tools) == 0 && document.DomainShape == nil) {
		return nil, newFailure(KindInvalidPayload, "project_tooling", "tooling manifest declares no tools", false, "declare at least one tool in "+projectToolingManifestPath)
	}
	declared := make([]ProjectDeclaredTool, 0, len(document.Tools))
	seen := map[string]bool{}
	bindings := map[FootprintCheck]ProjectToolCoverage{}
	for _, tool := range document.Tools {
		if !projectToolID.MatchString(tool.ID) || seen[tool.ID] {
			return nil, newFailure(KindInvalidPayload, "project_tooling", "tooling manifest declares a tool without an id", false, "declare every tool's stable id in "+projectToolingManifestPath)
		}
		seen[tool.ID] = true
		if !projectToolingPlainInvocation(tool.Invocation) {
			return nil, newFailure(KindInvalidPayload, "project_tooling", fmt.Sprintf("tooling manifest tool %q declares no argv", tool.ID), false, "declare a splittable invocation in "+projectToolingManifestPath)
		}
		var coverage []ProjectToolCoverage
		if tool.Coverage != nil {
			if string(tool.Coverage) == "null" {
				return nil, fmt.Errorf("coverage must be an array")
			}
			if err := decodeStrictKnowledgeJSON(tool.Coverage, &coverage); err != nil {
				return nil, err
			}
			if len(coverage) > 64 {
				return nil, fmt.Errorf("coverage exceeds 64 entries")
			}
			local := map[FootprintCheck]bool{}
			for _, entry := range coverage {
				if !projectToolingAnchorValid(entry.Anchor) || !projectToolingPathValid(entry.RecipePath) || len(entry.CaseID) > 128 || !oracleCaseIDPattern.MatchString(entry.CaseID) {
					return nil, fmt.Errorf("tool %q has invalid coverage anchor, recipe_path or case_id", tool.ID)
				}
				if local[entry.Anchor] {
					return nil, fmt.Errorf("duplicate coverage anchor")
				}
				local[entry.Anchor] = true
				if prior, exists := bindings[entry.Anchor]; exists && prior != entry {
					return nil, fmt.Errorf("conflicting coverage binding")
				}
				bindings[entry.Anchor] = entry
			}
			if len(coverage) > 0 {
				if _, _, ok := projectToolingDirectCommand(tool.Invocation); !ok {
					return nil, fmt.Errorf("coverage requires a direct allowlisted command")
				}
			}
		}
		declared = append(declared, ProjectDeclaredTool{ID: tool.ID, Invocation: tool.Invocation, Coverage: coverage})
	}
	shape, err := parseDomainShapeTooling(document.DomainShape, declared)
	if err != nil {
		return nil, err
	}
	return &ProjectToolingManifest{Project: document.Project, Declared: declared, DomainShape: shape}, nil
}

func parseDomainShapeTooling(raw json.RawMessage, declared []ProjectDeclaredTool) (*DomainShapeTooling, error) {
	if raw == nil {
		return nil, nil
	}
	shape := &DomainShapeTooling{}
	if err := decodeStrictKnowledgeJSON(raw, shape); err != nil {
		return nil, err
	}
	if err := domainShapeJSON(raw, shape); err != nil {
		return nil, err
	}
	var fields map[string]json.RawMessage
	_ = json.Unmarshal(raw, &fields)
	for _, key := range []string{"boundary_check", "data_object_extractor", "config_extractor", "reference_check"} {
		if value, exists := fields[key]; exists {
			var id string
			if json.Unmarshal(value, &id) != nil || !projectToolID.MatchString(id) {
				return nil, fmt.Errorf("domain_shape %s must name a tool id", key)
			}
		}
	}
	if err := validateDomainShapeTooling(shape, declared); err != nil {
		return nil, err
	}
	return shape, nil
}

func projectToolingPlainInvocation(value string) bool {
	if len(value) == 0 || len(value) > 512 || len(strings.Fields(value)) == 0 || strings.ContainsAny(value, "'\"`\\$;|&<>()") {
		return false
	}
	for _, r := range value {
		if r < 32 || r == 127 {
			return false
		}
	}
	return true
}

// ProjectToolingInvocationDeclared reports the guard's exact argv equality:
// the declared invocation splits on whitespace into argv, and the lease
// command must equal one declared split element for element. The tooling
// schema and scripts/check-project-tooling.py forbid shell quoting and shell
// operators in an invocation, so the split is exact and no shell re-tokenizes
// the command.
func ProjectToolingInvocationDeclared(tooling *ProjectToolingManifest, command []string) bool {
	if tooling == nil {
		return false
	}
	for _, tool := range tooling.Declared {
		if slices.Equal(strings.Fields(tool.Invocation), command) {
			return true
		}
	}
	return false
}

// ProjectToolingDeclaredText renders the declared checks a refusal names, or
// the none-declared statement a Project without a manifest gets.
func ProjectToolingDeclaredText(tooling *ProjectToolingManifest) string {
	if tooling == nil || len(tooling.Declared) == 0 {
		return "the Project declares no checks in " + projectToolingManifestPath
	}
	parts := make([]string, 0, len(tooling.Declared))
	used := 0
	for _, tool := range tooling.Declared {
		part := tool.ID + " (" + tool.Invocation + ")"
		if used > 0 && used+len(part)+2 > projectToolingRefusalBudget {
			parts = append(parts, fmt.Sprintf("and %d more declared check(s)", len(tooling.Declared)-len(parts)))
			break
		}
		parts = append(parts, part)
		used += len(part) + 2
	}
	return "declared checks: " + strings.Join(parts, ", ")
}

// RefineProofManifestRequired reports whether the named action on the work
// item's pinned definition runs a guard that reads the Project tooling
// manifest, so the caller must resolve it before the action's transaction
// opens (CD-0192). record_delivery runs the guard wherever it is gated, and
// accept_worker_result runs the same admission at the delivery-admitting
// refinement step (CD-0198 D4). bind_evidence resolves the manifest on a size-capable
// pin that holds a recorded split, so the automatic integration discharge
// can keep only runs of declared tools. Every other action
// resolves nothing.
func RefineProofManifestRequired(ctx context.Context, s *Store, workID, actionID string) (bool, error) {
	if s == nil || s.db == nil {
		return false, newFailure(KindUnavailable, "project_tooling", "store is not open", false, "open the authority database")
	}
	entry, err := VerifyWorkflowInstanceDefinition(ctx, s, BuiltinWorkflowRegistry(), workID)
	if err != nil {
		return false, err
	}
	var currentStep string
	if err := s.db.QueryRowContext(ctx, `SELECT current_step FROM workflow_instances WHERE work_id=?`, workID).Scan(&currentStep); err != nil {
		return false, wrapFailure(KindUnavailable, "project_tooling", "cannot read the workflow step", true, "retry once the workflow projection is readable", err)
	}
	switch actionID {
	case "record_delivery":
		return workflowRefineProofGateActive(entry.Definition, currentStep), nil
	case "accept_worker_result":
		return workflowAcceptDeliveryAdmissionActive(entry.Definition, currentStep), nil
	case "bind_evidence":
		// The split-integration proof obligation is retired with the split
		// system (CD-0242 D6); bind_evidence carries no derived obligation.
		return false, nil
	default:
		return false, nil
	}
}

// ResolveWorkProjectTooling resolves the tooling manifest the work item's
// primary Project declares on its default ref. A nil manifest with a nil
// error reports that the Project declares none there: the work item has no
// primary Project, no registered repository, or no manifest at the ref. Git
// runs outside any store transaction; the caller resolves before the action's
// transaction opens (CD-0192, amending CD-0138 D3).
func ResolveWorkProjectTooling(ctx context.Context, s *Store, workID string) (*ProjectToolingManifest, error) {
	return resolveWorkProjectTooling(ctx, s, workID, ExecGitRunner{})
}

func resolveWorkProjectTooling(ctx context.Context, s *Store, workID string, runner GitRunner) (*ProjectToolingManifest, error) {
	if s == nil || s.db == nil {
		return nil, newFailure(KindUnavailable, "project_tooling", "store is not open", false, "open the authority database")
	}
	var projectID string
	err := s.db.QueryRowContext(ctx, `SELECT project_id FROM work_projects WHERE work_id=? AND role='primary' ORDER BY project_id LIMIT 1`, workID).Scan(&projectID)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, wrapFailure(KindUnavailable, "project_tooling", "cannot read the work item's Project", true, "retry once the database is readable", err)
	}
	var repoRoot string
	err = s.db.QueryRowContext(ctx, `SELECT normalized_value FROM project_locators WHERE kind=? AND project_id=? ORDER BY locator_id LIMIT 1`, LocatorCanonicalPath, projectID).Scan(&repoRoot)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, wrapFailure(KindUnavailable, "project_tooling", "cannot read the Project's repository locator", true, "retry once the database is readable", err)
	}
	defaultRef, err := worktreeAuditDefaultRef(ctx, runner, repoRoot, "")
	if err != nil {
		return nil, retitleFailure(err, "project_tooling")
	}
	commitRaw, err := runner.Run(ctx, repoRoot, "rev-parse", "--verify", defaultRef+"^{commit}")
	if err != nil {
		return nil, wrapFailure(KindGitUnreachable, "project_tooling", "cannot pin the default ref", false, "retry once the Project repository is reachable", err)
	}
	commit := strings.TrimSpace(string(commitRaw))
	if !regexp.MustCompile(`^[0-9a-f]{40}([0-9a-f]{24})?$`).MatchString(commit) {
		return nil, newFailure(KindGitUnreachable, "project_tooling", "default ref did not resolve to an immutable commit OID", false, "restore the Project's default ref before resolving tooling")
	}
	manifest, err := resolveProjectToolingAtCommit(ctx, runner, repoRoot, projectID, commit)
	var failure *Failure
	if err != nil && !failureAs(err, &failure) {
		return nil, wrapFailure(KindInvalidPayload, "project_tooling", "invalid tooling declaration: "+err.Error(), false, "repair "+projectToolingManifestPath+" and its coverage bindings on the Project's default branch", err)
	}
	return manifest, err
}

func resolveProjectToolingAtCommit(ctx context.Context, runner GitRunner, repoRoot, projectID, commit string) (*ProjectToolingManifest, error) {
	// ls-tree separates "absent at the ref" (empty output, exit 0) from an
	// unreachable repository (nonzero exit), which cat-file -e cannot: a
	// missing path inside an existing rev exits 128 there.
	listing, err := runner.Run(ctx, repoRoot, "ls-tree", commit, "--", projectToolingManifestPath)
	if err != nil {
		return nil, wrapFailure(KindGitUnreachable, "project_tooling", "cannot inspect "+projectToolingManifestPath+" on the default ref", false, "retry once the Project repository is reachable", err)
	}
	if len(strings.TrimSpace(string(listing))) == 0 {
		return nil, nil
	}
	file, raw, err := nativeGitFile(ctx, runner, repoRoot, commit, projectToolingManifestPath)
	if err != nil {
		return nil, wrapFailure(KindGitUnreachable, "project_tooling", "cannot read "+projectToolingManifestPath+" from the default ref", false, "retry once the Project repository is reachable", err)
	}
	manifest, err := ParseProjectToolingManifest(raw)
	if err != nil {
		return nil, err
	}
	if len(manifest.Declared) == 0 {
		return nil, newFailure(KindInvalidPayload, "project_tooling", "tooling manifest declares no tools", false, "declare at least one tool in "+projectToolingManifestPath)
	}
	manifest.SourceCommit, manifest.SourceProjectID, manifest.ManifestDigest = commit, projectID, file.SHA256
	seen := map[FootprintCheck]int{}
	files := map[string]NativeOracleFile{}
	contents := map[string][]byte{}
	var scenarioSources []string
	bytesRead := len(raw)
	read := func(name string) ([]byte, error) {
		if data, exists := contents[name]; exists {
			return data, nil
		}
		input, data, err := nativeGitFile(ctx, runner, repoRoot, commit, name)
		if err != nil {
			return nil, err
		}
		bytesRead += len(data)
		if bytesRead > nativeGoInputByteLimit || len(files) >= nativeGoInputFileLimit {
			return nil, fmt.Errorf("resolved tooling inputs exceed 4096 files or 33554432 bytes")
		}
		files[name], contents[name] = input, data
		return data, nil
	}
	for _, tool := range manifest.Declared {
		for _, coverage := range tool.Coverage {
			if index, exists := seen[coverage.Anchor]; exists {
				if !ProjectToolingCoverageLineage(tool.Invocation, manifest.ResolvedCoverage[index].Recipe) {
					return nil, fmt.Errorf("tool %s has no direct lineage to shared coverage recipe %s", tool.ID, coverage.RecipePath)
				}
				if err := projectToolingAnchorCase(coverage.Anchor, manifest.ResolvedCoverage[index].Recipe, read, scenarioSources, tool.Invocation); err != nil {
					return nil, fmt.Errorf("shared coverage anchor does not match tool %s: %w", tool.ID, err)
				}
				continue
			}
			recipeFile, recipeRaw, err := nativeGitFile(ctx, runner, repoRoot, commit, coverage.RecipePath)
			if err != nil {
				return nil, fmt.Errorf("resolve coverage recipe %s: %w", coverage.RecipePath, err)
			}
			bytesRead += len(recipeRaw)
			if bytesRead > 32*1024*1024 {
				return nil, fmt.Errorf("resolved tooling inputs exceed 33554432 bytes")
			}
			recipe, err := ResolveNativeRecipeCase(recipeRaw, coverage.CaseID)
			if err != nil {
				return nil, err
			}
			if !ProjectToolingCoverageLineage(tool.Invocation, recipe) {
				return nil, fmt.Errorf("tool %s has no direct lineage to coverage recipe %s", tool.ID, coverage.RecipePath)
			}
			resolved := ProjectResolvedCoverage{Anchor: coverage.Anchor, RecipeSource: WorkContextReadingSource{Kind: "repository_file", ProjectID: projectID, Path: coverage.RecipePath, CommitOID: commit}, RecipeDigest: recipeFile.SHA256, CaseID: coverage.CaseID, Recipe: recipe, RecipeRaw: slices.Clone(recipeRaw)}
			for _, name := range slices.Concat(recipe.HarnessFiles, recipe.FixtureFiles) {
				if _, err := read(name); err != nil {
					return nil, fmt.Errorf("resolve coverage input %s: %w", name, err)
				}
				resolved.Files = append(resolved.Files, files[name])
			}
			if coverage.Anchor.Kind == "scenario" && scenarioSources == nil {
				scenarioSources, err = projectToolingScenarioSources(ctx, runner, repoRoot, commit)
				if err != nil {
					return nil, err
				}
			}
			if err := projectToolingAnchorCase(coverage.Anchor, recipe, read, scenarioSources, tool.Invocation); err != nil {
				return nil, fmt.Errorf("coverage anchor %s:%s does not match case %s: %w", coverage.Anchor.Kind, coverage.Anchor.Value, coverage.CaseID, err)
			}
			seen[coverage.Anchor] = len(manifest.ResolvedCoverage)
			manifest.ResolvedCoverage = append(manifest.ResolvedCoverage, resolved)
		}
	}
	return manifest, nil
}
