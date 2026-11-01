// Package pm1fixture is the shared PM1 acceptance fixture. Its data source is
// .concord/scenarios/product-memory-query.v1.json. It exists so multiple corpus runners
// share one implementation rather than re-seeding the same dataset each time.
//
// The package does not import testing: it returns errors and lets callers
// decide how to surface them. The exported Seed and SeedKnowledge functions
// build the SQLite + Git knowledge state the PM1 corpus runners query.
package pm1fixture

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"time"

	"github.com/sharper-flow/concord/internal/store"
	"github.com/sharper-flow/concord/internal/store/storetest"
)

// Corpus mirrors the JSON shape of .concord/scenarios/product-memory-query.v1.json.
// The struct tags are preserved exactly so callers that read the same file
// can interop without bespoke translations.
type Corpus struct {
	Fixtures struct {
		Products []struct {
			ID    string `json:"id"`
			Name  string `json:"name"`
			Stage string `json:"stage"`
		} `json:"products"`
		Projects []struct {
			ID       string   `json:"id"`
			Name     string   `json:"name"`
			Products []string `json:"products"`
		} `json:"projects"`
		Work []struct {
			ID       string `json:"id"`
			Product  string `json:"product"`
			Projects []struct {
				ID   string `json:"id"`
				Role string `json:"role"`
			} `json:"projects"`
			Lifecycle  string `json:"lifecycle"`
			Priority   int64  `json:"priority"`
			CreatedAt  string `json:"created_at"`
			TerminalAt string `json:"terminal_at"`
		} `json:"work"`
		Relations []struct {
			Kind   string `json:"kind"`
			Source string `json:"source"`
			Target string `json:"target"`
		} `json:"relations"`
		Events []Event `json:"events"`
	} `json:"fixtures"`
	Scenarios []struct {
		ID              string         `json:"id"`
		QueryID         string         `json:"query_id"`
		Input           map[string]any `json:"input"`
		DependsOn       []string       `json:"depends_on"`
		FixtureOverride map[string]any `json:"fixture_override"`
		Expected        struct {
			Authority  string
			Assertions []struct {
				Path, Op string
				Value    any
			}
		}
		ExpectedError struct {
			Kind         string   `json:"kind"`
			CandidateIDs []string `json:"candidate_ids"`
		} `json:"expected_error"`
	} `json:"scenarios"`
}

// Event is one entry in corpus.Fixtures.Events. It is exported so a runner can
// build ad-hoc fixtureEvent-equivalent values without copying the helper.
type Event struct {
	Work         string   `json:"work"`
	Seq          int      `json:"seq"`
	From         *string  `json:"from"`
	To           *string  `json:"to"`
	Kind         string   `json:"kind"`
	Actor        string   `json:"actor"`
	EvidenceRefs []string `json:"evidence_refs"`
}

// GitKnowledge carries the resolved compaction home of the seeded corpus.
type GitKnowledge struct {
	Home store.KnowledgeHome
}

const (
	FixtureDomainRegistryContentHash = "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	// FixtureRootDomainID is the root Domain SeedCommittedProductDomain
	// commits for product-1, the Product nearly every caller seeds.
	FixtureRootDomainID = "product-root:fixture-product-1"
)

// SeedCommittedProductDomain designates a real git knowledge home for a
// Product and commits its Domain registry and fixture law without building
// any projection. The registry then exists only as committed content, which
// is the state a fresh operator store is in: the production path derives it
// from the committed home, or a Product-changing approval refuses for a
// registry nobody has read. It returns the home so a test can move its head
// or inspect the derived hash.
func SeedCommittedProductDomain(ctx context.Context, s *store.Store, productID, homeProjectID, dir string) (store.KnowledgeHome, error) {
	// A registry's root Domain is named for its Product key.
	root := "product-root:fixture-" + productID
	registry := store.KnowledgeDomainRegistry{
		SchemaVersion: "1.0",
		ProductKey:    "fixture-" + productID,
		RootDomainID:  root,
		Domains: []store.KnowledgeDomain{{DomainID: root, Name: "Fixture root",
			Purpose: "Product law fixture", Status: "current", ArchitectureRelations: []store.KnowledgeArchitectureRelation{}}},
	}
	return SeedProductDomainRegistry(ctx, s, productID, homeProjectID, dir, registry)
}

// SeedProductDomainRegistry is SeedCommittedProductDomain with the caller's
// committed registry: the same designated home, the same committed fixture
// law, and no projection rows, for fixtures that need Domains beyond the
// single current root (deprecated or wrong-Domain refusal cases).
func SeedProductDomainRegistry(ctx context.Context, s *store.Store, productID, homeProjectID, dir string, registry store.KnowledgeDomainRegistry) (store.KnowledgeHome, error) {
	repo, err := initKnowledgeRepo(dir)
	if err != nil {
		return store.KnowledgeHome{}, err
	}
	const decisionPath = ".concord/law/decisions/CD-0001-fixture-law.md"
	decision := canonicalKnowledgeNote("CD-0001", registry.RootDomainID)
	if err := writeKnowledgeFile(repo, decisionPath, decision); err != nil {
		return store.KnowledgeHome{}, fmt.Errorf("pm1fixture: write fixture decision: %w", err)
	}
	if err := WriteKnowledgeShards(repo, registry); err != nil {
		return store.KnowledgeHome{}, fmt.Errorf("pm1fixture: write domain registry: %w", err)
	}
	if _, err := commitKnowledgeRepo(repo, "committed Product Domain registry"); err != nil {
		return store.KnowledgeHome{}, fmt.Errorf("pm1fixture: commit knowledge repo: %w", err)
	}
	home := store.KnowledgeHome{HomeProjectID: homeProjectID, HomeLocatorID: "domain-locator-" + productID, RepoPath: repo, HeadRef: "HEAD"}
	if err := authorizeKnowledgeProductHome(ctx, s, productID, home); err != nil {
		return store.KnowledgeHome{}, fmt.Errorf("pm1fixture: authorize product home: %w", err)
	}
	return home, nil
}

// Load reads .concord/scenarios/product-memory-query.v1.json from the repository root
// and decodes it into a Corpus. The lookup is anchored to this file's source
// location so the package can move without breaking the relative path.
func Load() (Corpus, error) {
	var corpus Corpus

	_, file, _, ok := runtime.Caller(0)
	if !ok {
		return corpus, fmt.Errorf("pm1fixture: runtime.Caller failed")
	}
	path := filepath.Join(filepath.Dir(file), "..", "..", ".concord", "scenarios", "product-memory-query.v1.json")
	data, err := os.ReadFile(path) //nolint:gosec // runtime.Caller anchors this code-owned scenario path inside the repository.
	if err != nil {
		return corpus, fmt.Errorf("pm1fixture: read %s: %w", path, err)
	}
	if err := json.Unmarshal(data, &corpus); err != nil {
		return corpus, fmt.Errorf("pm1fixture: decode %s: %w", path, err)
	}
	return corpus, nil
}

// OpenTemp opens a fresh, migrated Concord store inside dir. The caller owns
// the returned *store.Store and is responsible for closing it. This mirrors
// the historical openTemp helper used inside internal/store tests.
func OpenTemp(dir string) (*store.Store, error) {
	s, err := storetest.Open(dir)
	if err != nil {
		return nil, fmt.Errorf("pm1fixture: open store: %w", err)
	}
	return s, nil
}

// Seed writes the products, projects, work items, lifecycle transitions, and
// relations described by c into s. The audience commitment is recorded as
// operator_only: PM1's fixture has stage but no audience, and operator_only is
// the conservative contract-valid default.
func Seed(ctx context.Context, s *store.Store, c Corpus) error {
	if err := seedProductsAndProjects(ctx, s, c); err != nil {
		return err
	}
	versions := map[string]int64{}
	if err := seedWorkItems(ctx, s, c, versions); err != nil {
		return err
	}
	if err := seedWorkTransitions(ctx, s, c, versions); err != nil {
		return err
	}
	return seedWorkRelations(ctx, s, c, versions)
}

func (c Corpus) eventFor(work string, seq int) Event {
	for _, event := range c.Fixtures.Events {
		if event.Work == work && event.Seq == seq {
			return event
		}
	}
	return Event{}
}

func seedProductsAndProjects(ctx context.Context, s *store.Store, c Corpus) error {
	// Adapter-only audience choice: PM1's fixture has stage but no audience;
	// operator_only is the conservative contract-valid default.
	events := []store.Event{}
	expected := map[store.SubjectRef]int64{}
	for _, p := range c.Fixtures.Products {
		events = append(events, fixtureEvent("create-"+p.ID, "product.created", 1, store.SubjectProduct, p.ID, "operator", "2026-08-01T00:00:00Z", map[string]any{"display_name": p.Name, "stage_maturity": p.Stage, "stage_audience_commitment": "operator_only"}))
		expected[store.VersionRef(store.SubjectProduct, p.ID)] = 0
	}
	for _, p := range c.Fixtures.Projects {
		events = append(events, fixtureEvent("create-"+p.ID, "project.created", 2, store.SubjectProject, p.ID, "operator", "2026-08-01T00:00:00Z", map[string]any{"display_name": p.Name}))
		expected[store.VersionRef(store.SubjectProject, p.ID)] = 0
	}
	for _, p := range c.Fixtures.Products {
		version := int64(1)
		for _, project := range c.Fixtures.Projects {
			for _, productID := range project.Products {
				if productID != p.ID {
					continue
				}
				role := "secondary"
				if version == 1 {
					role = "primary"
				}
				events = append(events, fixtureEvent(fmt.Sprintf("membership-%s-%s", p.ID, project.ID), "product_project.added", 1, store.SubjectProduct, p.ID, "operator", "2026-08-01T00:00:00Z", map[string]any{"product_id": p.ID, "project_id": project.ID, "role": role, "reason": "fixture adapter", "expected_version": version, "resulting_version": version + 1}))
				version++
			}
		}
	}
	if err := store.ApplyOperation(ctx, s, store.Operation{Events: events, ExpectedVersions: expected}); err != nil {
		return fmt.Errorf("pm1fixture: seed products and projects: %w", err)
	}
	return nil
}

func seedWorkItems(ctx context.Context, s *store.Store, c Corpus, versions map[string]int64) error {
	for _, w := range c.Fixtures.Work {
		versions[w.ID] = 1
		created := c.eventFor(w.ID, 1)
		actor := created.Actor
		if actor == "" {
			actor = "operator"
		}
		to := "needed"
		if created.To != nil {
			to = *created.To
		}
		workEvents := []store.Event{fixtureEvent("create-"+w.ID, "work.created", 4, store.SubjectWorkItem, w.ID, actor, w.CreatedAt, map[string]any{"work_kind": "task", "title": w.ID, "priority": w.Priority, "from": created.From, "to": to})}
		// Membership is written the way the capture path writes it: one
		// work.memberships_replaced carrying the whole set, not one
		// work_project.added per Project. Both are legal, and they consume a
		// different number of aggregate versions - N against 1 - so a fixture
		// using the second shape puts every multi-Project work item at a
		// version no real capture could produce, and any scenario reading that
		// version exercises arithmetic the production path never performs.
		if len(w.Projects) > 0 {
			memberships := make([]map[string]any, len(w.Projects))
			for i, p := range w.Projects {
				memberships[i] = map[string]any{"project_id": p.ID, "role": p.Role}
			}
			workEvents = append(workEvents, fixtureEvent("membership-"+w.ID, "work.memberships_replaced", 1, store.SubjectWorkItem, w.ID, "operator", w.CreatedAt, map[string]any{"memberships": memberships, "expected_version": int64(1), "resulting_version": int64(2)}))
			versions[w.ID] = 2
		}
		if err := store.ApplyOperation(ctx, s, store.Operation{Events: workEvents, ExpectedVersions: map[store.SubjectRef]int64{store.VersionRef(store.SubjectWorkItem, w.ID): 0}}); err != nil {
			return fmt.Errorf("pm1fixture: create %s: %w", w.ID, err)
		}
		scope, err := s.ProductsForWorkIDs(ctx, []string{w.ID})
		if err != nil {
			return fmt.Errorf("pm1fixture: derive fixture Product scope for %s: %w", w.ID, err)
		}
		found := false
		for _, product := range scope[w.ID] {
			if product == w.Product {
				found = true
				break
			}
		}
		if !found {
			return fmt.Errorf("pm1fixture: fixture Product %q is outside derived scope for work %q", w.Product, w.ID)
		}
	}
	return nil
}

func seedWorkTransitions(ctx context.Context, s *store.Store, c Corpus, versions map[string]int64) error {
	var events []store.Event
	for _, w := range c.Fixtures.Work {
		if w.ID == "work-cross" || w.ID == "work-done" {
			started := c.eventFor(w.ID, 2)
			from, to := fixtureTransitionStates(started, "needed", "in_progress")
			events = append(events, fixtureEvent("event-"+w.ID+"-started", "work.transitioned", 1, store.SubjectWorkItem, w.ID, started.Actor, "2026-08-02T09:00:00Z", map[string]any{"from": from, "to": to, "reason": "fixture started", "expected_version": versions[w.ID], "resulting_version": versions[w.ID] + 1}))
			versions[w.ID]++
			if w.ID == "work-cross" {
				continue
			}
			completed := c.eventFor(w.ID, 3)
			from, to = fixtureTransitionStates(completed, "in_progress", "completed")
			evidence := completed.EvidenceRefs
			if evidence == nil {
				evidence = []string{}
			}
			events = append(events, fixtureEvent("event-work-done-completed", "work.transitioned", 1, store.SubjectWorkItem, w.ID, completed.Actor, w.TerminalAt, map[string]any{"from": from, "to": to, "reason": "fixture completed", "evidence_refs": evidence, "expected_version": versions[w.ID], "resulting_version": versions[w.ID] + 1}))
			versions[w.ID]++
			continue
		}
		if w.Lifecycle == "in_progress" {
			events = append(events, fixtureEvent("transition-"+w.ID, "work.transitioned", 1, store.SubjectWorkItem, w.ID, "operator", "2026-08-02T09:00:00Z", map[string]any{"from": "needed", "to": "in_progress", "reason": "fixture lifecycle", "expected_version": versions[w.ID], "resulting_version": versions[w.ID] + 1}))
			versions[w.ID]++
		} else if w.Lifecycle == "cancelled" {
			events = append(events, fixtureEvent("transition-"+w.ID, "work.transitioned", 1, store.SubjectWorkItem, w.ID, "operator", w.TerminalAt, map[string]any{"from": "needed", "to": "cancelled", "reason": "fixture lifecycle", "expected_version": versions[w.ID], "resulting_version": versions[w.ID] + 1}))
			versions[w.ID]++
		}
	}
	if len(events) > 0 {
		if err := store.ApplyOperation(ctx, s, store.Operation{Events: events, ExpectedVersions: map[store.SubjectRef]int64{}}); err != nil {
			return fmt.Errorf("pm1fixture: seed work transitions: %w", err)
		}
	}
	return nil
}

func fixtureTransitionStates(event Event, from, to string) (string, string) {
	if event.From != nil {
		from = *event.From
	}
	if event.To != nil {
		to = *event.To
	}
	return from, to
}

func seedWorkRelations(ctx context.Context, s *store.Store, c Corpus, versions map[string]int64) error {
	for _, r := range c.Fixtures.Relations {
		switch r.Kind {
		case "depends_on":
			event := fixtureEvent("relation-depends", "relation.added", 1, store.SubjectWorkItem, r.Target, "operator", "2026-08-06T00:00:00Z", map[string]any{"from": r.Target, "to": r.Source, "kind": "blocks", "reason": "fixture dependency", "expected_version": versions[r.Target], "resulting_version": versions[r.Target] + 1})
			if err := store.ApplyOperation(ctx, s, store.Operation{Events: []store.Event{event}}); err != nil {
				return fmt.Errorf("pm1fixture: seed depends_on relation %s->%s: %w", r.Source, r.Target, err)
			}
			versions[r.Target]++
		case "supersedes":
			event := fixtureEvent("relation-supersedes", "work.superseded", 1, store.SubjectWorkItem, r.Target, "operator", "2026-08-06T00:00:00Z", map[string]any{"successor": r.Source, "superseded": r.Target, "reason": "fixture supersession", "expected_version": versions[r.Target], "resulting_version": versions[r.Target] + 1})
			if err := store.ApplyOperation(ctx, s, store.Operation{Events: []store.Event{event}}); err != nil {
				return fmt.Errorf("pm1fixture: seed supersedes relation %s->%s: %w", r.Source, r.Target, err)
			}
			versions[r.Target]++
		}
	}
	return nil
}

// SeedKnowledge initializes a throwaway git repository containing the
// canonical work note, registers it as prod-alpha's designated home, and links
// the completed work item to its canonical note. The returned GitKnowledge
// exposes the resolved home.
//
// dir must be a caller-owned directory whose lifetime the caller controls, so
// the throwaway repository is reclaimed with it; test callers pass t.TempDir().
func SeedKnowledge(ctx context.Context, s *store.Store, c Corpus, dir string) (GitKnowledge, error) {
	repo, err := initKnowledgeRepo(dir)
	if err != nil {
		return GitKnowledge{}, fmt.Errorf("pm1fixture: init knowledge repo: %w", err)
	}
	workPath := ".concord/docs/work/2026-08-03-auth-release.md"
	if err := writeKnowledgeFile(repo, workPath, canonicalWorkNote("work-done", "2026-08-03T12:00:00Z")); err != nil {
		return GitKnowledge{}, fmt.Errorf("pm1fixture: write work note: %w", err)
	}
	if err := writeKnowledgeFile(repo, ".concord/law/decisions/CD-0002-state-authority.md", canonicalKnowledgeNote("CD-0002", "product-root:fixture-product")); err != nil {
		return GitKnowledge{}, fmt.Errorf("pm1fixture: write fixture law: %w", err)
	}
	if err := writeFixtureRegistry(repo); err != nil {
		return GitKnowledge{}, err
	}
	commit, err := commitKnowledgeRepo(repo, "accepted PM1 corpus")
	if err != nil {
		return GitKnowledge{}, fmt.Errorf("pm1fixture: commit knowledge repo: %w", err)
	}
	home := store.KnowledgeHome{HomeProjectID: "proj-web", HomeLocatorID: "repo-alpha-web", RepoPath: repo, HeadRef: "HEAD"}
	if err := authorizeKnowledgeProductHome(ctx, s, "prod-alpha", home); err != nil {
		return GitKnowledge{}, fmt.Errorf("pm1fixture: authorize product home: %w", err)
	}
	var version int64
	if err := s.DatabaseForTesting().QueryRowContext(ctx, `SELECT version FROM work_items WHERE id = 'work-done'`).Scan(&version); err != nil {
		return GitKnowledge{}, fmt.Errorf("pm1fixture: read work-done version: %w", err)
	}
	if err := store.PublishCompactionLink(ctx, s, store.CompactionLinkRequest{EventID: "corpus-compaction-work-done", WorkID: "work-done", ExpectedVersion: version, Actor: "operator", OccurredAt: time.Date(2026, 8, 7, 0, 0, 0, 0, time.UTC), Home: home, CommitOID: commit, NotePath: workPath, Reason: "accepted corpus fixture"}); err != nil {
		return GitKnowledge{}, fmt.Errorf("pm1fixture: publish compaction link: %w", err)
	}
	return GitKnowledge{Home: home}, nil
}

// writeFixtureRegistry commits the fixture Domain registry shard the retained
// Domain resolution reads at its existing repository path.
func writeFixtureRegistry(repo string) error {
	registry := store.KnowledgeDomainRegistry{
		SchemaVersion: "1.0",
		ProductKey:    "fixture-product",
		RootDomainID:  "product-root:fixture-product",
		Domains: []store.KnowledgeDomain{{DomainID: "product-root:fixture-product", Name: "Fixture product",
			Purpose: "Product-wide fixture law", Status: "current", ArchitectureRelations: []store.KnowledgeArchitectureRelation{}}},
	}
	return WriteKnowledgeShards(repo, registry)
}

// SeedGoverningRequirement declares one CD-0035 governing requirement against a
// seeded Project. It stays separate from Seed to keep base Project versions stable.
func SeedGoverningRequirement(ctx context.Context, s *store.Store, projectID, requirementRef, reason string) error {
	var current int64
	if err := s.DatabaseForTesting().QueryRowContext(ctx, `SELECT version FROM projects WHERE id=?`, projectID).Scan(&current); err != nil {
		return fmt.Errorf("pm1fixture: read Project %s version: %w", projectID, err)
	}
	event := fixtureEvent(
		"governing-requirement-"+projectID+"-"+requirementRef,
		"project.governing_requirement_declared",
		1,
		store.SubjectProject,
		projectID,
		"operator",
		"2026-08-07T12:00:00Z",
		map[string]any{
			"project_id":        projectID,
			"requirement_ref":   requirementRef,
			"reason":            reason,
			"expected_version":  current,
			"resulting_version": current + 1,
		},
	)
	if err := store.ApplyOperation(ctx, s, store.Operation{Events: []store.Event{event}, ExpectedVersions: map[store.SubjectRef]int64{store.VersionRef(store.SubjectProject, projectID): current}}); err != nil {
		return fmt.Errorf("pm1fixture: declare governing requirement %s on %s: %w", requirementRef, projectID, err)
	}
	return nil
}

func fixtureEvent(id, kind string, payloadVersion int, subject store.SubjectType, subjectID, actor, occurred string, payload map[string]any) store.Event {
	raw, err := json.Marshal(payload)
	if err != nil {
		panic(err)
	}
	when, err := time.Parse(time.RFC3339, occurred)
	if err != nil {
		when = time.Date(2026, 8, 7, 12, 0, 0, 0, time.UTC)
	}
	return store.Event{EventID: id, Kind: kind, SubjectType: subject, SubjectID: subjectID, Actor: actor, OccurredAt: when, PayloadVersion: payloadVersion, Payload: raw}
}

func initKnowledgeRepo(dir string) (string, error) {
	repo, err := os.MkdirTemp(dir, "concord-pm1-knowledge-")
	if err != nil {
		return "", err
	}
	if _, err := runKnowledgeGit(repo, "init", "--initial-branch=main"); err != nil {
		return "", err
	}
	if _, err := runKnowledgeGit(repo, "config", "user.email", "test@example.invalid"); err != nil {
		return "", err
	}
	if _, err := runKnowledgeGit(repo, "config", "user.name", "Concord Test"); err != nil {
		return "", err
	}
	return repo, nil
}

func writeKnowledgeFile(repo, path, content string) error {
	full := filepath.Join(repo, filepath.FromSlash(path))
	if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil { //nolint:gosec // fixture repositories contain public Git content with repository-standard directory permissions.
		return err
	}
	if err := os.WriteFile(full, []byte(content), 0o644); err != nil { //nolint:gosec // fixture repositories contain public Git content with repository-standard file permissions.
		return err
	}
	return nil
}

func commitKnowledgeRepo(repo, message string) (string, error) {
	if _, err := runKnowledgeGit(repo, "add", "--", "."); err != nil {
		return "", err
	}
	if _, err := runKnowledgeGit(repo, "commit", "--quiet", "--date", "2026-08-07T00:00:00Z", "-m", message, "--author", "Concord Test <test@example.invalid>"); err != nil {
		return "", err
	}
	out, err := runKnowledgeGit(repo, "rev-parse", "HEAD")
	if err != nil {
		return "", err
	}
	return trimNewline(out), nil
}

// runKnowledgeGit invokes git inside repo, returns its combined stdout. The
// fixed GIT_AUTHOR_DATE/GIT_COMMITTER_DATE pair makes the corpus reproduce
// identical commit OIDs across runs, which the fixture relies on for
// commit-alias resolution.
func runKnowledgeGit(repo string, args ...string) (string, error) {
	cmd := exec.Command("git", append([]string{"-C", repo}, args...)...) //nolint:gosec // this fixture passes code-owned git argv values directly without a shell.
	cmd.Env = append(os.Environ(), "GIT_AUTHOR_DATE=2026-08-07T00:00:00Z", "GIT_COMMITTER_DATE=2026-08-07T00:00:00Z")
	out, err := cmd.CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("git %v: %v\n%s", args, err, out)
	}
	return string(out), nil
}

func trimNewline(s string) string {
	for len(s) > 0 && (s[len(s)-1] == '\n' || s[len(s)-1] == '\r') {
		s = s[:len(s)-1]
	}
	return s
}

func canonicalWorkNote(id, completed string) string {
	return CanonicalWorkNote(id, completed, "completed", "proj-web")
}

// CanonicalWorkNote builds the canonical durable work note for a terminal work
// item. terminalState and project vary because compaction verifies the note's
// terminal state against the work item's own lifecycle, so publishing a note for
// cancelled work needs a note that says cancelled.
func CanonicalWorkNote(id, completed, terminalState, project string) string {
	outcome := "shipped"
	if terminalState != "completed" {
		outcome = "abandoned"
	}
	return "---\n" +
		"concord_work_id: " + id + "\n" +
		"work_type: implementation\n" +
		"title: Auth release\n" +
		"completed_at: " + completed + "\n" +
		"outcome_tag: " + outcome + "\n" +
		"lesson_tags: [sqlite, state-authority]\n" +
		"terminal_state: " + terminalState + "\n" +
		"priority: 2\n" +
		"summary: Bounded summary\n" +
		"product_ids: [prod-alpha]\n" +
		"project_ids: [" + project + "]\n" +
		"domain_ids: [auth]\n" +
		"tag_ids: [auth, release]\n" +
		"---\n\nDurable note.\n"
}

func canonicalKnowledgeNote(id, homeDomainID string) string {
	front := `{"id":"` + id + `","title":"Fixture law","authority":{"tier":"legislated","legislated_by":"work-0f1e2d3c4b5a6978","contract_version":1},"home_domain_id":"` + homeDomainID + `","applies_to_domain_ids":[],"product_wide_rationale":"The fixture registry declares no child Domain, so this law has no child home available."}`
	return "---\n" + front + "\n---\n\nDurable fixture law.\n"
}

func authorizeKnowledgeLocator(ctx context.Context, s *store.Store, home store.KnowledgeHome) (err error) {
	if _, err := s.DatabaseForTesting().ExecContext(ctx, `INSERT INTO write_guard(active) VALUES(1)`); err != nil {
		return err
	}
	defer func() {
		_, cleanupErr := s.DatabaseForTesting().ExecContext(ctx, `DELETE FROM write_guard`)
		if err == nil {
			err = cleanupErr
		}
	}()
	if _, err := s.DatabaseForTesting().ExecContext(ctx, `INSERT OR IGNORE INTO projects(id,display_name,version,created_at,updated_at) VALUES(?, ?, 1, 'now', 'now')`, home.HomeProjectID, home.HomeProjectID); err != nil {
		return err
	}
	if _, err := s.DatabaseForTesting().ExecContext(ctx, `INSERT OR IGNORE INTO project_locators(locator_id,project_id,kind,locator_value,normalized_value,created_at,updated_at) VALUES(?, ?, 'canonical_path', ?, ?, 'now', 'now')`, home.HomeLocatorID, home.HomeProjectID, home.RepoPath, home.RepoPath); err != nil {
		return err
	}
	return nil
}

func authorizeKnowledgeProductHome(ctx context.Context, s *store.Store, productID string, home store.KnowledgeHome) (err error) {
	if err := authorizeKnowledgeLocator(ctx, s, home); err != nil {
		return err
	}
	if _, err := s.DatabaseForTesting().ExecContext(ctx, `INSERT INTO write_guard(active) VALUES(1)`); err != nil {
		return err
	}
	defer func() {
		_, cleanupErr := s.DatabaseForTesting().ExecContext(ctx, `DELETE FROM write_guard`)
		if err == nil {
			err = cleanupErr
		}
	}()
	if _, err := s.DatabaseForTesting().ExecContext(ctx, `INSERT OR IGNORE INTO products(id,display_name,stage_maturity,stage_audience_commitment,version,created_at,updated_at) VALUES(?, ?, 'prototype', 'operator_only', 1, 'now', 'now')`, productID, productID); err != nil {
		return err
	}
	if _, err := s.DatabaseForTesting().ExecContext(ctx, `INSERT OR IGNORE INTO product_knowledge_homes(product_id,project_id,locator_id) VALUES(?, ?, ?)`, productID, home.HomeProjectID, home.HomeLocatorID); err != nil {
		return err
	}
	if _, err := s.DatabaseForTesting().ExecContext(ctx, `DELETE FROM write_guard`); err != nil {
		return err
	}
	return nil
}
