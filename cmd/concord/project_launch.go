package main

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/sharper-flow/concord/internal/launcher"
	"github.com/sharper-flow/concord/internal/launcher/storeport"
	"github.com/sharper-flow/concord/internal/store"
)

// projectTokenPattern bounds the first positional argument when it is treated
// as a project reference. It matches Product and Project identifiers; paths
// are normalized before use, so slashes are allowed by the launch path itself.
var projectTokenPattern = sessionIdentity

func isProjectLaunchSyntax(args []string) bool {
	if len(args) == 0 {
		return false
	}
	// Reject tokens that are already operator command names so that a typo
	// like `concord clien-register` falls through to the normal unsupported-
	// arguments diagnostic instead of being interpreted as a project.
	if _, _, ok := routeCommand([]string{args[0]}); ok {
		return false
	}
	// Only treat the token as a project reference when it is passed as a
	// path. This prevents arbitrary unrecognized words (e.g. a mistyped JSON
	// command) from being swallowed by the project launcher.
	if !looksLikeProjectPath(args[0]) {
		return false
	}
	if len(args) >= 2 {
		// Only `project -- prompt...` is accepted; anything else falls
		// through to the JSON dispatcher or the unsupported-arguments error.
		if args[1] != "--" {
			return false
		}
	}
	return true
}

func looksLikeProjectPath(token string) bool {
	if token == "" {
		return false
	}
	if filepath.IsAbs(token) {
		return true
	}
	if strings.ContainsAny(token, "/\\~") {
		return true
	}
	return token == "." || token == ".."
}

// runProjectLaunchCommand resolves a local project reference to its Product
// and starts a Concord orchestrator session. If the reference is not
// registered, it runs an interactive Product membership + stage selection flow.
func runProjectLaunchCommand(args []string, in io.Reader, out, errOut io.Writer, terminal bool) int {
	if !terminal {
		writeDiagnostic(errOut, "concord: project launch requires an interactive TTY")
		return 2
	}

	projectRef := args[0]
	leadPrompt := ""
	if len(args) >= 2 && args[1] == "--" {
		leadPrompt = strings.Join(args[2:], " ")
	}

	absPath, err := filepath.Abs(projectRef)
	if err != nil {
		writeDiagnostic(errOut, fmt.Sprintf("concord: cannot resolve project path: %v", err))
		return 2
	}
	if info, err := os.Stat(absPath); err != nil || !info.IsDir() { //nolint:gosec // G703: path is cleaned via filepath.Abs and verified to be a directory; project launch is filesystem-bound by design
		writeDiagnostic(errOut, fmt.Sprintf("concord: not a project directory: %s", projectRef))
		return 2
	}

	path, err := databasePath()
	if err != nil {
		writeDiagnostic(errOut, err.Error())
		return 1
	}
	if _, statErr := os.Stat(path); os.IsNotExist(statErr) {
		writeDiagnostic(errOut, "concord: no authority database; run operator setup first")
		return 1
	} else if statErr != nil {
		writeDiagnostic(errOut, "concord: database path is unavailable: "+statErr.Error())
		return 1
	}

	s, openErr := store.Open(context.Background(), path)
	if openErr != nil {
		writeDiagnostic(errOut, openErr.Error())
		return 1
	}
	defer func() { _ = s.Close() }()

	ctx := context.Background()
	resolution, err := s.ResolveProject(ctx, absPath, absPath)
	if err != nil {
		failure, ok := err.(*store.Failure)
		if ok && failure.Kind == store.KindUnknownScope {
			productID, regErr := runProjectRegisterInteractive(ctx, s, absPath, in, out, errOut)
			if regErr != nil {
				writeDiagnostic(errOut, "concord: registration aborted: "+regErr.Error())
				return 1
			}
			return projectLaunchSessionStarter(productID, "", leadPrompt, in, out, errOut, terminal)
		}
		writeDiagnostic(errOut, "concord: cannot resolve project: "+err.Error())
		return 1
	}

	_, productIDs, err := s.ScopeVersion(ctx, resolution.ProjectID)
	if err != nil {
		writeDiagnostic(errOut, "concord: cannot read project membership: "+err.Error())
		return 1
	}
	if len(productIDs) == 0 {
		writeDiagnostic(errOut, "concord: project has no Product membership")
		return 1
	}
	if len(productIDs) > 1 {
		writeDiagnostic(errOut, fmt.Sprintf("concord: project belongs to multiple Products (%s); select one in the launcher", strings.Join(productIDs, ", ")))
		return 1
	}
	return projectLaunchSessionStarter(productIDs[0], "", leadPrompt, in, out, errOut, terminal)
}

// projectLaunchSessionStarter is the project-launch entry point to the shared
// session runner. It is a variable so tests can capture the resolved identity
// without starting the real host.
var projectLaunchSessionStarter = func(productID, workID, leadPrompt string, in io.Reader, out, errOut io.Writer, terminal bool) int {
	return orchestratorSession(productID, workID, leadPrompt, in, out, errOut, terminal, deriveSessionBoot, runOpenCode, hostLaneAgentIdentity, hostOrchestratorIdentity)
}

// runProjectRegisterInteractive runs the approved registration flow for an
// unregistered project. It returns the selected Product identity on success.
func runProjectRegisterInteractive(ctx context.Context, s *store.Store, absPath string, in io.Reader, out, errOut io.Writer) (string, error) {
	reader := bufio.NewReader(in)
	base := filepath.Base(absPath)

	if _, err := fmt.Fprintf(out, "No Concord Project registered for %s.\n", absPath); err != nil {
		return "", err
	}

	products, err := listProducts(ctx, s)
	if err != nil {
		return "", err
	}

	var productID string
	var projectID string
	if len(products) > 0 {
		choice, err := promptLine(reader, out, "Create a new Product [new] or add to an existing one [existing]?", "new")
		if err != nil {
			return "", err
		}
		switch strings.ToLower(strings.TrimSpace(choice)) {
		case "", "new", "n":
			productID, projectID, err = createNewProductInteractive(ctx, s, reader, out, base)
		case "existing", "e":
			productID, projectID, err = createProjectForExistingProductInteractive(ctx, s, reader, out, products)
		default:
			return "", fmt.Errorf("unrecognized choice: %s", choice)
		}
		if err != nil {
			return "", err
		}
	} else {
		var err error
		productID, projectID, err = createNewProductInteractive(ctx, s, reader, out, base)
		if err != nil {
			return "", err
		}
	}

	// Resolve the repository information so we can record the canonical path
	// locator. Re-use ResolveProject even though it will fail on scope; the
	// Repository field is populated before the failure is returned.
	resolution, _ := s.ResolveProject(ctx, absPath, absPath)

	projectVersion, err := s.EntityVersion(ctx, store.SubjectProject, projectID)
	if err != nil {
		return "", fmt.Errorf("cannot read Project version: %w", err)
	}

	if resolution.Repository.CanonicalPath != "" {
		locator := store.ProjectLocator{Kind: store.LocatorCanonicalPath, Value: resolution.Repository.CanonicalPath}
		if err := s.AddProjectLocator(ctx, projectID, locator, projectVersion); err != nil {
			return "", fmt.Errorf("cannot add canonical path locator: %w", err)
		}
		projectVersion++
	}
	if resolution.Repository.GitRemote != "" {
		locator := store.ProjectLocator{Kind: store.LocatorGitRemote, Value: resolution.Repository.GitRemote}
		if err := s.AddProjectLocator(ctx, projectID, locator, projectVersion); err != nil {
			return "", fmt.Errorf("cannot add git remote locator: %w", err)
		}
	}

	if _, err := fmt.Fprintf(out, "Registered Project %s under Product %s.\n", projectID, productID); err != nil {
		return "", err
	}
	return productID, nil
}

func listProducts(ctx context.Context, s *store.Store) ([]launcher.ProductRow, error) {
	port := storeport.New(s)
	snap, err := port.Read(ctx, launcher.ReadRequest{Kind: launcher.ReadPortfolio, Limit: 100})
	if err != nil {
		return nil, err
	}
	return snap.Rows, nil
}

func selectExistingProductInteractive(reader *bufio.Reader, out io.Writer, products []launcher.ProductRow) (string, error) {
	if _, err := fmt.Fprintln(out, "Existing Products:"); err != nil {
		return "", err
	}
	for _, p := range products {
		if _, err := fmt.Fprintf(out, "  %s - %s\n", p.ID, p.Name); err != nil {
			return "", err
		}
	}
	choice, err := promptLine(reader, out, "Select Product ID", "")
	if err != nil {
		return "", err
	}
	choice = strings.TrimSpace(choice)
	for _, p := range products {
		if p.ID == choice {
			return p.ID, nil
		}
	}
	return "", fmt.Errorf("selected Product does not exist: %s", choice)
}

func createProjectForExistingProductInteractive(ctx context.Context, s *store.Store, reader *bufio.Reader, out io.Writer, products []launcher.ProductRow) (string, string, error) {
	productID, err := selectExistingProductInteractive(reader, out, products)
	if err != nil {
		return "", "", err
	}
	version, err := s.EntityVersion(ctx, store.SubjectProduct, productID)
	if err != nil {
		return "", "", fmt.Errorf("cannot read Product version: %w", err)
	}
	projectID, err := promptLine(reader, out, "Project ID", "")
	if err != nil {
		return "", "", err
	}
	displayName, err := promptLine(reader, out, "Project display name", "")
	if err != nil {
		return "", "", err
	}
	role, err := promptLine(reader, out, "Membership role (primary/secondary)", "primary")
	if err != nil {
		return "", "", err
	}
	_, err = s.CreateProjectForProduct(ctx, store.ProjectCreation{
		ProjectID:              strings.TrimSpace(projectID),
		DisplayName:            strings.TrimSpace(displayName),
		ProductID:              productID,
		Role:                   strings.TrimSpace(role),
		ExpectedProductVersion: version,
	})
	if err != nil {
		return "", "", fmt.Errorf("cannot create Project: %w", err)
	}
	return productID, strings.TrimSpace(projectID), nil
}

func createNewProductInteractive(ctx context.Context, s *store.Store, reader *bufio.Reader, out io.Writer, base string) (string, string, error) {
	productID, err := promptLine(reader, out, "Product ID", base)
	if err != nil {
		return "", "", err
	}
	displayName, err := promptLine(reader, out, "Display name", base)
	if err != nil {
		return "", "", err
	}
	maturity, err := promptLine(reader, out, "Stage maturity (prototype/alpha/beta/production)", "prototype")
	if err != nil {
		return "", "", err
	}
	audience, err := promptLine(reader, out, "Audience commitment (operator_only/limited/public)", "operator_only")
	if err != nil {
		return "", "", err
	}
	projectID, err := promptLine(reader, out, "Project ID", base)
	if err != nil {
		return "", "", err
	}
	projectDisplayName, err := promptLine(reader, out, "Project display name", base)
	if err != nil {
		return "", "", err
	}

	_, err = s.CreateProductWithProject(ctx, store.ProductCreation{
		ProductID:               strings.TrimSpace(productID),
		DisplayName:             strings.TrimSpace(displayName),
		StageMaturity:           strings.TrimSpace(maturity),
		StageAudienceCommitment: strings.TrimSpace(audience),
		ProjectID:               strings.TrimSpace(projectID),
		ProjectDisplayName:      strings.TrimSpace(projectDisplayName),
		Role:                    "primary",
	})
	if err != nil {
		return "", "", err
	}
	return strings.TrimSpace(productID), strings.TrimSpace(projectID), nil
}

func promptLine(reader *bufio.Reader, out io.Writer, label, defaultValue string) (string, error) {
	if defaultValue != "" {
		if _, err := fmt.Fprintf(out, "%s [%s]: ", label, defaultValue); err != nil {
			return "", err
		}
	} else {
		if _, err := fmt.Fprintf(out, "%s: ", label); err != nil {
			return "", err
		}
	}
	line, err := reader.ReadString('\n')
	if err != nil {
		return "", err
	}
	line = strings.TrimSpace(line)
	if line == "" {
		return defaultValue, nil
	}
	return line, nil
}
