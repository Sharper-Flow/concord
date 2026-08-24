package store

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/url"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// LocatorKind is deliberately closed. Display names, paths, and clone names
// are attributes; only these normalized locators participate in identity.
type LocatorKind string

const (
	LocatorGitRemote     LocatorKind = "git_remote"
	LocatorCanonicalPath LocatorKind = "canonical_path"
)

type ProjectLocator struct {
	ID              string      `json:"locator_id"`
	ProjectID       string      `json:"project_id"`
	Kind            LocatorKind `json:"kind"`
	Value           string      `json:"value"`
	NormalizedValue string      `json:"normalized_value"`
}

type HostRepository struct {
	CanonicalPath string
	GitRemote     string
}

// GitRunner is the test seam for host verification. Implementations receive
// argv as separate values; a shell command string is never accepted.
type GitRunner interface {
	Run(context.Context, string, ...string) ([]byte, error)
}

type ExecGitRunner struct{}

func (ExecGitRunner) Run(ctx context.Context, dir string, args ...string) ([]byte, error) {
	if dir == "" {
		return nil, fmt.Errorf("empty git directory")
	}
	command := append([]string{"-C", dir}, args...)
	return exec.CommandContext(ctx, "git", command...).Output()
}

type ProjectResolution struct {
	ProjectID  string
	Repository HostRepository
	Locators   []ProjectLocator
	// MainWorktree reports that the resolved working tree is the repository's
	// main checkout rather than a linked worktree (CD-0008 D1). It is false
	// when no resolver ran, so the zero value never blocks a grant.
	MainWorktree bool
}

// ScopeVersion is a structural membership watermark. It changes only when the
// sorted Product↔Project authority changes, not when unrelated events append or
// when a repository moves on disk.
func (s *Store) ScopeVersion(ctx context.Context, projectID string) (string, []string, error) {
	return scopeVersion(ctx, s.db, projectID)
}

func scopeVersion(ctx context.Context, q queryer, projectID string) (string, []string, error) {
	rows, err := q.QueryContext(ctx, `SELECT product_id,role FROM product_projects WHERE project_id=? ORDER BY product_id,role`, projectID)
	if err != nil {
		return "", nil, wrapFailure(KindUnavailable, "scope_version", "cannot read Product scope", true, "retry once the database is readable", err)
	}
	defer rows.Close()
	var pairs, products []string
	for rows.Next() {
		var product, role string
		if err := rows.Scan(&product, &role); err != nil {
			return "", nil, err
		}
		pairs = append(pairs, product+"|"+role)
		products = append(products, product)
	}
	if err := rows.Err(); err != nil {
		return "", nil, err
	}
	digest := sha256.Sum256([]byte(strings.Join(pairs, "\n")))
	return "sha256:" + hex.EncodeToString(digest[:]), orderedStrings(products), nil
}

func normalizePath(value string) (string, error) {
	if value == "" || strings.ContainsRune(value, '\x00') {
		return "", newFailure(KindInvalidFilter, "project_locator", "path is empty or contains NUL", false, "supply a valid repository path")
	}
	abs, err := filepath.Abs(value)
	if err != nil {
		return "", wrapFailure(KindInvalidFilter, "project_locator", "cannot canonicalize repository path", false, "supply a reachable repository path", err)
	}
	resolved, err := filepath.EvalSymlinks(abs)
	if err != nil {
		return "", wrapFailure(KindGitUnreachable, "project_locator", "cannot resolve repository path", true, "restore access to the repository and retry", err)
	}
	return filepath.Clean(resolved), nil
}

func normalizeRemote(value string) (string, error) {
	value = strings.TrimSpace(value)
	if value == "" || strings.ContainsRune(value, '\x00') {
		return "", newFailure(KindInvalidFilter, "project_locator", "git remote is empty or contains NUL", false, "supply a valid git remote")
	}
	for _, r := range value {
		if r < 0x20 || r == 0x7f || r == ' ' || r == '\t' {
			return "", newFailure(KindInvalidFilter, "project_locator", "git remote contains control or whitespace", false, "supply a valid git remote")
		}
	}
	if strings.Contains(value, "\\") {
		return "", newFailure(KindInvalidFilter, "project_locator", "git remote contains a backslash", false, "supply a normalized git remote")
	}
	if at := strings.IndexByte(value, ':'); at > 0 && !strings.Contains(value[:at], "/") && !strings.Contains(value[:at], "://") {
		value = "ssh://" + value[:at] + "/" + value[at+1:]
	}
	u, err := url.Parse(value)
	if err != nil || u.Scheme == "" || u.Host == "" {
		return "", newFailure(KindInvalidFilter, "project_locator", "git remote is not a supported URL", false, "use a remote URL or scp-style SSH remote")
	}
	scheme := strings.ToLower(u.Scheme)
	if scheme != "ssh" && scheme != "git" && scheme != "http" && scheme != "https" {
		return "", newFailure(KindInvalidFilter, "project_locator", "git remote scheme is not supported", false, "use ssh, git, http, or https")
	}
	host := strings.ToLower(u.Host)
	path := strings.Trim(strings.TrimSpace(u.EscapedPath()), "/")
	path = strings.TrimSuffix(path, ".git")
	if path == "" || strings.Contains(path, "..") {
		return "", newFailure(KindInvalidFilter, "project_locator", "git remote path is invalid", false, "supply a repository remote")
	}
	path = strings.ToLower(path)
	user := ""
	if u.User != nil {
		user = u.User.Username() + "@"
	}
	return scheme + "://" + user + host + "/" + path, nil
}

// ProjectCanonicalPath returns the Project's canonical_path locator value,
// normalized. It is the read seam for callers that need the registered
// repository location (for example the worktree locator verb, issue #316);
// identity never derives from the returned path (CD-0008 D1).
func (s *Store) ProjectCanonicalPath(ctx context.Context, projectID string) (string, error) {
	if s == nil || s.db == nil {
		return "", newFailure(KindUnavailable, "project_locator", "store is not open", false, "open the authority database")
	}
	var normalized string
	err := s.db.QueryRowContext(ctx, `SELECT normalized_value FROM project_locators WHERE kind=? AND project_id=? ORDER BY locator_id LIMIT 1`, LocatorCanonicalPath, projectID).Scan(&normalized)
	if err != nil {
		if err == sql.ErrNoRows {
			return "", newFailure(KindUnknownScope, "project_locator", "Project has no canonical_path locator", false, "register the repository's canonical path locator")
		}
		return "", wrapFailure(KindUnavailable, "project_locator", "cannot read Project locators", true, "retry once the database is readable", err)
	}
	return normalized, nil
}

func (s *Store) AddProjectLocator(ctx context.Context, projectID string, locator ProjectLocator, expectedVersion int64) error {
	return s.applyLocatorEvent(ctx, projectID, "project.locator_added", locator, expectedVersion)
}

func (s *Store) UpdateProjectLocator(ctx context.Context, projectID string, locator ProjectLocator, expectedVersion int64) error {
	return s.applyLocatorEvent(ctx, projectID, "project.locator_updated", locator, expectedVersion)
}

func (s *Store) RemoveProjectLocator(ctx context.Context, projectID, locatorID string, expectedVersion int64) error {
	if locatorID == "" {
		return newFailure(KindInvalidFilter, "project_locator", "locator_id is required", false, "supply the stable locator ID")
	}
	return s.applyLocatorEvent(ctx, projectID, "project.locator_removed", ProjectLocator{ID: locatorID, ProjectID: projectID}, expectedVersion)
}

func (s *Store) applyLocatorEvent(ctx context.Context, projectID, kind string, locator ProjectLocator, expected int64) error {
	if projectID == "" || expected < 1 {
		return newFailure(KindInvalidOperation, "project_locator", "project and positive expected version are required", false, "reload the Project and retry")
	}
	if locator.ID == "" {
		raw := make([]byte, 16)
		if _, err := rand.Read(raw); err != nil {
			return err
		}
		locator.ID = hex.EncodeToString(raw)
	}
	if kind != "project.locator_removed" {
		normalized, err := NormalizeProjectLocator(locator.Kind, locator.Value)
		if err != nil {
			return err
		}
		locator.NormalizedValue = normalized
		if locator.Value == "" {
			locator.Value = normalized
		}
	}
	if locator.Kind != LocatorGitRemote && locator.Kind != LocatorCanonicalPath && kind != "project.locator_removed" {
		return newFailure(KindInvalidFilter, "project_locator", "locator kind is not recognized", false, "use git_remote or canonical_path")
	}
	payload, _ := json.Marshal(map[string]any{
		"project_id": projectID, "locator_id": locator.ID, "kind": locator.Kind,
		"value": locator.Value, "normalized_value": locator.NormalizedValue,
		"expected_version": expected, "resulting_version": expected + 1,
	})
	return ApplyOperation(ctx, s, Operation{Events: []Event{{EventID: locatorEventID(kind, locator.ID), Kind: kind, SubjectType: SubjectProject, SubjectID: projectID, Actor: "operator", OccurredAt: s.now(), PayloadVersion: 1, Payload: payload}}, ExpectedVersions: map[SubjectRef]int64{VersionRef(SubjectProject, projectID): expected}})
}

func locatorEventID(kind, id string) string {
	return fmt.Sprintf("%s:%s:%d", kind, id, time.Now().UnixNano())
}

func NormalizeProjectLocator(kind LocatorKind, value string) (string, error) {
	switch kind {
	case LocatorGitRemote:
		return normalizeRemote(value)
	case LocatorCanonicalPath:
		return normalizePath(value)
	default:
		return "", newFailure(KindInvalidFilter, "project_locator", "locator kind is not recognized", false, "use git_remote or canonical_path")
	}
}

func (s *Store) ProjectLocators(ctx context.Context, projectID string) ([]ProjectLocator, error) {
	return projectLocators(ctx, s.db, projectID)
}

func projectLocators(ctx context.Context, q queryer, projectID string) ([]ProjectLocator, error) {
	rows, err := q.QueryContext(ctx, `SELECT locator_id,project_id,kind,locator_value,normalized_value FROM project_locators WHERE project_id=? ORDER BY kind,normalized_value,locator_id`, projectID)
	if err != nil {
		return nil, wrapFailure(KindUnavailable, "project_locator", "cannot read Project locators", true, "retry once the database is readable", err)
	}
	defer rows.Close()
	var out []ProjectLocator
	for rows.Next() {
		var l ProjectLocator
		if err := rows.Scan(&l.ID, &l.ProjectID, &l.Kind, &l.Value, &l.NormalizedValue); err != nil {
			return nil, err
		}
		out = append(out, l)
	}
	return out, rows.Err()
}

func (s *Store) ResolveProject(ctx context.Context, directory, worktree string) (ProjectResolution, error) {
	return s.ResolveProjectWithRunner(ctx, directory, worktree, ExecGitRunner{})
}

func (s *Store) ResolveProjectWithRunner(ctx context.Context, directory, worktree string, runner GitRunner) (ProjectResolution, error) {
	return resolveProjectWithRunner(ctx, s.db, directory, worktree, runner)
}

func resolveProjectWithRunner(ctx context.Context, q queryer, directory, worktree string, runner GitRunner) (ProjectResolution, error) {
	if runner == nil {
		return ProjectResolution{}, newFailure(KindInvalidOperation, "resolve_project", "git runner is nil", false, "provide a git runner")
	}
	paths := []string{worktree, directory}
	var root string
	for _, candidate := range paths {
		if candidate == "" {
			continue
		}
		out, err := runner.Run(ctx, candidate, "rev-parse", "--show-toplevel")
		if err == nil && strings.TrimSpace(string(out)) != "" {
			root = strings.TrimSpace(string(out))
			break
		}
	}
	if root == "" {
		return ProjectResolution{}, newFailure(KindGitUnreachable, "resolve_project", "signed directory/worktree is not a git repository", true, "run the client from a reachable git worktree")
	}
	canonical, err := normalizePath(root)
	if err != nil {
		return ProjectResolution{}, err
	}
	mainWorktree, err := resolveMainWorktree(ctx, runner, canonical)
	if err != nil {
		return ProjectResolution{}, err
	}
	remoteOut, remoteErr := runner.Run(ctx, canonical, "config", "--get", "remote.origin.url")
	remote := ""
	if remoteErr == nil && strings.TrimSpace(string(remoteOut)) != "" {
		remote, err = normalizeRemote(string(remoteOut))
		if err != nil {
			return ProjectResolution{}, err
		}
	}
	host := HostRepository{CanonicalPath: canonical, GitRemote: remote}
	locators := []ProjectLocator{}
	if remote != "" {
		locators, err = matchProjectLocators(ctx, q, LocatorGitRemote, remote)
		if err != nil {
			return ProjectResolution{}, err
		}
	}
	pathMatches, err := matchProjectLocators(ctx, q, LocatorCanonicalPath, canonical)
	if err != nil {
		return ProjectResolution{}, err
	}
	locators = append(locators, pathMatches...)
	ids := map[string]bool{}
	for _, l := range locators {
		ids[l.ProjectID] = true
	}
	if len(ids) == 0 {
		return ProjectResolution{Repository: host, MainWorktree: mainWorktree}, newFailure(KindUnknownScope, "resolve_project", "git repository has no known Project locator", false, "register a canonical_path or git_remote locator")
	}
	if len(ids) != 1 {
		candidates := make([]string, 0, len(ids))
		for id := range ids {
			candidates = append(candidates, id)
		}
		sort.Strings(candidates)
		f := newFailure(KindAmbiguousScope, "resolve_project", "git repository matches multiple Projects", false, "remove the conflicting locator or select one stable Project")
		f.CandidateIDs = candidates
		return ProjectResolution{Repository: host, Locators: locators, MainWorktree: mainWorktree}, f
	}
	var id string
	for candidate := range ids {
		id = candidate
	}
	return ProjectResolution{ProjectID: id, Repository: host, Locators: locators, MainWorktree: mainWorktree}, nil
}

// resolveMainWorktree reports whether the working tree at root is the
// repository's main checkout rather than a linked worktree (CD-0008 D1).
// git-dir and git-common-dir identify the same directory only in the main
// working tree; a linked worktree keeps its own git-dir under the common one.
// Both outputs may be relative to root, so they are resolved and symlink
// normalized before comparison. The probe fails closed: if git cannot report
// the topology, resolution itself fails.
func resolveMainWorktree(ctx context.Context, runner GitRunner, root string) (bool, error) {
	gitDirOut, gitDirErr := runner.Run(ctx, root, "rev-parse", "--git-dir")
	commonDirOut, commonDirErr := runner.Run(ctx, root, "rev-parse", "--git-common-dir")
	if gitDirErr != nil || commonDirErr != nil {
		return false, newFailure(KindGitUnreachable, "resolve_project", "cannot determine worktree topology", true, "run the client from a reachable git worktree")
	}
	same := func(raw string) (string, error) {
		value := strings.TrimSpace(raw)
		if value == "" {
			return "", newFailure(KindGitUnreachable, "resolve_project", "git reported an empty worktree path", true, "run the client from a reachable git worktree")
		}
		if !filepath.IsAbs(value) {
			value = filepath.Join(root, value)
		}
		resolved, err := filepath.EvalSymlinks(value)
		if err != nil {
			return "", newFailure(KindGitUnreachable, "resolve_project", "git worktree path is not reachable", true, "run the client from a reachable git worktree")
		}
		return resolved, nil
	}
	gitDir, err := same(string(gitDirOut))
	if err != nil {
		return false, err
	}
	commonDir, err := same(string(commonDirOut))
	if err != nil {
		return false, err
	}
	return gitDir == commonDir, nil
}

func matchProjectLocators(ctx context.Context, q queryer, kind LocatorKind, normalized string) ([]ProjectLocator, error) {
	rows, err := q.QueryContext(ctx, `SELECT locator_id,project_id,kind,locator_value,normalized_value FROM project_locators WHERE kind=? AND normalized_value=? ORDER BY project_id`, kind, normalized)
	if err != nil {
		return nil, wrapFailure(KindUnavailable, "resolve_project", "cannot read Project locators", true, "retry once the database is readable", err)
	}
	defer rows.Close()
	var out []ProjectLocator
	for rows.Next() {
		var l ProjectLocator
		if err := rows.Scan(&l.ID, &l.ProjectID, &l.Kind, &l.Value, &l.NormalizedValue); err != nil {
			return nil, err
		}
		out = append(out, l)
	}
	return out, rows.Err()
}

type projectLocatorPayload struct {
	ProjectID        string      `json:"project_id"`
	LocatorID        string      `json:"locator_id"`
	Kind             LocatorKind `json:"kind"`
	Value            string      `json:"value"`
	NormalizedValue  string      `json:"normalized_value"`
	ExpectedVersion  int64       `json:"expected_version"`
	ResultingVersion int64       `json:"resulting_version"`
}

func decodeProjectLocator(event Event) (projectLocatorPayload, error) {
	var payload projectLocatorPayload
	if err := decodePayload(event, &payload); err != nil {
		return payload, err
	}
	if payload.ProjectID != event.SubjectID || payload.LocatorID == "" || payload.ExpectedVersion < 1 || payload.ResultingVersion != payload.ExpectedVersion+1 {
		return payload, newFailure(KindInvalidPayload, "fold_event", "Project locator payload has invalid identity or version evidence", false, "supply the event Project and consecutive versions")
	}
	if event.Kind != "project.locator_removed" {
		if payload.Kind != LocatorGitRemote && payload.Kind != LocatorCanonicalPath || payload.Value == "" || payload.NormalizedValue == "" {
			return payload, newFailure(KindInvalidPayload, "fold_event", "Project locator payload has invalid kind or value", false, "supply a normalized git_remote or canonical_path")
		}
		normalized, err := NormalizeProjectLocator(payload.Kind, payload.Value)
		if err != nil {
			return payload, err
		}
		if normalized != payload.NormalizedValue {
			return payload, newFailure(KindInvalidPayload, "fold_event", "Project locator normalization is not deterministic", false, "use the normalized locator value")
		}
	}
	return payload, nil
}

func foldProjectLocatorAdded(ctx context.Context, tx *sql.Tx, event Event) error {
	if err := checkSubject(event, SubjectProject); err != nil {
		return err
	}
	p, err := decodeProjectLocator(event)
	if err != nil {
		return err
	}
	if err := insertProjectLocator(ctx, tx, p, event.OccurredAt); err != nil {
		return err
	}
	return bumpVersion(ctx, tx, "projects", event, p.ExpectedVersion, p.ResultingVersion, "Project")
}

func foldProjectLocatorUpdated(ctx context.Context, tx *sql.Tx, event Event) error {
	if err := checkSubject(event, SubjectProject); err != nil {
		return err
	}
	p, err := decodeProjectLocator(event)
	if err != nil {
		return err
	}
	result, err := tx.ExecContext(ctx, `UPDATE project_locators SET kind=?,locator_value=?,normalized_value=?,updated_at=? WHERE locator_id=? AND project_id=?`, p.Kind, p.Value, p.NormalizedValue, event.OccurredAt.UTC().Format(time.RFC3339Nano), p.LocatorID, p.ProjectID)
	if err != nil {
		if isUniqueViolation(err) {
			return newFailure(KindMembershipConflict, "fold_event", "Project locator is already claimed", false, "choose a unique normalized locator")
		}
		return wrapFailure(KindUnavailable, "fold_event", "cannot update Project locator", true, "retry once the database is writable", err)
	}
	if n, _ := result.RowsAffected(); n != 1 {
		return newFailure(KindProjectionNotFound, "fold_event", "Project locator does not exist", false, "add the locator before updating it")
	}
	return bumpVersion(ctx, tx, "projects", event, p.ExpectedVersion, p.ResultingVersion, "Project")
}

func foldProjectLocatorRemoved(ctx context.Context, tx *sql.Tx, event Event) error {
	if err := checkSubject(event, SubjectProject); err != nil {
		return err
	}
	p, err := decodeProjectLocator(event)
	if err != nil {
		return err
	}
	result, err := tx.ExecContext(ctx, `DELETE FROM project_locators WHERE locator_id=? AND project_id=?`, p.LocatorID, p.ProjectID)
	if err != nil {
		return wrapFailure(KindUnavailable, "fold_event", "cannot remove Project locator", true, "retry once the database is writable", err)
	}
	if n, _ := result.RowsAffected(); n != 1 {
		return newFailure(KindProjectionNotFound, "fold_event", "Project locator does not exist", false, "reload the Project locators")
	}
	return bumpVersion(ctx, tx, "projects", event, p.ExpectedVersion, p.ResultingVersion, "Project")
}

func insertProjectLocator(ctx context.Context, tx *sql.Tx, p projectLocatorPayload, occurred time.Time) error {
	stamp := occurred.UTC().Format(time.RFC3339Nano)
	_, err := tx.ExecContext(ctx, `INSERT INTO project_locators(locator_id,project_id,kind,locator_value,normalized_value,created_at,updated_at) VALUES(?,?,?,?,?,?,?)`, p.LocatorID, p.ProjectID, p.Kind, p.Value, p.NormalizedValue, stamp, stamp)
	if err == nil {
		return nil
	}
	if isUniqueViolation(err) {
		return newFailure(KindMembershipConflict, "fold_event", "Project locator is already claimed", false, "choose a unique normalized locator")
	}
	if isForeignKeyViolation(err) {
		return newFailure(KindProjectionNotFound, "fold_event", "Project does not exist", false, "create the Project before adding a locator")
	}
	return wrapFailure(KindUnavailable, "fold_event", "cannot add Project locator", true, "retry once the database is writable", err)
}
