// Package pm1fixture is the shared PM1 acceptance fixture. Its data source is
// scenarios/product-memory-query.v1.json. It exists so multiple corpus runners
// share one implementation rather than re-seeding the same dataset each time.
//
// The package does not import testing: it returns errors and lets callers
// decide how to surface them. The exported Seed and SeedKnowledge functions
// build the same SQLite + Git knowledge state that internal/store previously
// built in package-private form.
package pm1fixture

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
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

// Corpus mirrors the JSON shape of scenarios/product-memory-query.v1.json.
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
		Events    []Event `json:"events"`
		Knowledge []struct {
			ID          string `json:"id"`
			Path        string `json:"path"`
			Commit      string `json:"commit"`
			ContentHash string `json:"content_hash"`
		} `json:"knowledge"`
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

// GitKnowledge carries the resolved KnowledgeHome plus commit/content-hash
// aliases that fixture expected-value placeholders resolve against.
type GitKnowledge struct {
	Home        store.KnowledgeHome
	CommitAlias map[string]string
	HashAlias   map[string]string
}

const (
	FixtureDomainRegistryContentHash = "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	FixtureRootDomainID              = "root"
)

// SeedCurrentProductDomain adds the minimal current Domain projection needed
// by Product-changing workflow fixtures. The projection is deliberately
// reusable so agent fixtures exercise the same current registry lookup as the
// authority without duplicating SQL in each test package.
func SeedCurrentProductDomain(ctx context.Context, s *store.Store, productID, homeProjectID string) error {
	tx, err := s.DatabaseForTesting().BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("pm1fixture: begin Domain fixture transaction: %w", err)
	}
	defer tx.Rollback()
	homeLocatorID := "domain-locator-" + productID
	if _, err := tx.ExecContext(ctx, `INSERT INTO fold_guard(active) VALUES(1)`); err != nil {
		return fmt.Errorf("pm1fixture: enable Domain fixture fold guard: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO project_locators(locator_id,project_id,kind,locator_value,normalized_value,created_at,updated_at) VALUES(?,?,?,?,?,'fixture','fixture')`, homeLocatorID, homeProjectID, "canonical_path", "/fixture/"+productID, "/fixture/"+productID); err != nil {
		return fmt.Errorf("pm1fixture: seed Product law locator: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO product_knowledge_homes(product_id,project_id,locator_id) VALUES(?,?,?)`, productID, homeProjectID, homeLocatorID); err != nil {
		return fmt.Errorf("pm1fixture: seed Product law home: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO domain_registries(product_id,home_project_id,home_locator_id,product_key,root_domain_id,schema_version,content_hash,scanned_commit_oid) VALUES(?,?,?,?,?,'1.0',?,'fixture')`, productID, homeProjectID, homeLocatorID, "fixture-"+productID, FixtureRootDomainID, FixtureDomainRegistryContentHash); err != nil {
		return fmt.Errorf("pm1fixture: seed Domain registry: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO domains(home_project_id,home_locator_id,product_id,domain_id,name,purpose,parent_domain_id,status,registry_content_hash,scanned_commit_oid) VALUES(?,?,?,?,?,?,?,?,?,?)`, homeProjectID, homeLocatorID, productID, FixtureRootDomainID, "Fixture root", "Product law fixture", nil, "current", FixtureDomainRegistryContentHash, "fixture"); err != nil {
		return fmt.Errorf("pm1fixture: seed root Domain: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM fold_guard`); err != nil {
		return fmt.Errorf("pm1fixture: disable Domain fixture fold guard: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("pm1fixture: commit Domain fixture: %w", err)
	}
	return nil
}

// Load reads scenarios/product-memory-query.v1.json from the repository root
// and decodes it into a Corpus. The lookup is anchored to this file's source
// location so the package can move without breaking the relative path.
func Load() (Corpus, error) {
	var corpus Corpus

	_, file, _, ok := runtime.Caller(0)
	if !ok {
		return corpus, fmt.Errorf("pm1fixture: runtime.Caller failed")
	}
	path := filepath.Join(filepath.Dir(file), "..", "..", "scenarios", "product-memory-query.v1.json")
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
	eventFor := func(work string, seq int) Event {
		for _, event := range c.Fixtures.Events {
			if event.Work == work && event.Seq == seq {
				return event
			}
		}
		return Event{}
	}
	// Adapter-only audience choice: PM1's fixture has stage but no audience;
	// operator_only is the conservative contract-valid default.
	events := []store.Event{}
	expected := map[store.SubjectRef]int64{}
	for _, p := range c.Fixtures.Products {
		events = append(events, fixtureEvent("create-"+p.ID, "product.created", store.SubjectProduct, p.ID, "operator", "2026-08-01T00:00:00Z", map[string]any{"display_name": p.Name, "stage_maturity": p.Stage, "stage_audience_commitment": "operator_only"}))
		expected[store.VersionRef(store.SubjectProduct, p.ID)] = 0
	}
	for _, p := range c.Fixtures.Projects {
		events = append(events, fixtureEvent("create-"+p.ID, "project.created", store.SubjectProject, p.ID, "operator", "2026-08-01T00:00:00Z", map[string]any{"display_name": p.Name}))
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
				events = append(events, fixtureEvent(fmt.Sprintf("membership-%s-%s", p.ID, project.ID), "product_project.added", store.SubjectProduct, p.ID, "operator", "2026-08-01T00:00:00Z", map[string]any{"product_id": p.ID, "project_id": project.ID, "role": role, "reason": "fixture adapter", "expected_version": version, "resulting_version": version + 1}))
				version++
			}
		}
	}
	if err := store.ApplyOperation(ctx, s, store.Operation{Events: events, ExpectedVersions: expected}); err != nil {
		return fmt.Errorf("pm1fixture: seed products and projects: %w", err)
	}
	versions := map[string]int64{}
	for _, w := range c.Fixtures.Work {
		versions[w.ID] = 1
		created := eventFor(w.ID, 1)
		actor := created.Actor
		if actor == "" {
			actor = "operator"
		}
		to := "needed"
		if created.To != nil {
			to = *created.To
		}
		workEvents := []store.Event{fixtureEvent("create-"+w.ID, "work.created", store.SubjectWorkItem, w.ID, actor, w.CreatedAt, map[string]any{"work_kind": "task", "title": w.ID, "priority": w.Priority, "from": created.From, "to": to})}
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
			workEvents = append(workEvents, fixtureEvent("membership-"+w.ID, "work.memberships_replaced", store.SubjectWorkItem, w.ID, "operator", w.CreatedAt, map[string]any{"memberships": memberships, "expected_version": int64(1), "resulting_version": int64(2)}))
			versions[w.ID] = 2
		}
		if err := store.ApplyOperation(ctx, s, store.Operation{Events: workEvents, ExpectedVersions: map[store.SubjectRef]int64{store.VersionRef(store.SubjectWorkItem, w.ID): 0}}); err != nil {
			return fmt.Errorf("pm1fixture: create %s: %w", w.ID, err)
		}
		scope, err := s.ProductsForWork(ctx, w.ID)
		if err != nil {
			return fmt.Errorf("pm1fixture: derive fixture Product scope for %s: %w", w.ID, err)
		}
		found := false
		for _, product := range scope.Products {
			if product.ID == w.Product {
				found = true
				break
			}
		}
		if !found {
			return fmt.Errorf("pm1fixture: fixture Product %q is outside derived scope for work %q", w.Product, w.ID)
		}
	}
	events = nil
	for _, w := range c.Fixtures.Work {
		if w.ID == "work-cross" {
			started := eventFor(w.ID, 2)
			actor, from, to := started.Actor, "needed", "in_progress"
			if started.From != nil {
				from = *started.From
			}
			if started.To != nil {
				to = *started.To
			}
			events = append(events, fixtureEvent("event-work-cross-started", "work.transitioned", store.SubjectWorkItem, w.ID, actor, "2026-08-02T09:00:00Z", map[string]any{"from": from, "to": to, "reason": "fixture started", "expected_version": versions[w.ID], "resulting_version": versions[w.ID] + 1}))
			versions[w.ID]++
			continue
		}
		if w.ID == "work-done" {
			started := eventFor(w.ID, 2)
			actor, from, to := started.Actor, "needed", "in_progress"
			if started.From != nil {
				from = *started.From
			}
			if started.To != nil {
				to = *started.To
			}
			events = append(events, fixtureEvent("event-work-done-started", "work.transitioned", store.SubjectWorkItem, w.ID, actor, "2026-08-02T09:00:00Z", map[string]any{"from": from, "to": to, "reason": "fixture started", "expected_version": versions[w.ID], "resulting_version": versions[w.ID] + 1}))
			versions[w.ID]++
			completed := eventFor(w.ID, 3)
			actor, from, to = completed.Actor, "in_progress", "completed"
			if completed.From != nil {
				from = *completed.From
			}
			if completed.To != nil {
				to = *completed.To
			}
			evidence := completed.EvidenceRefs
			if evidence == nil {
				evidence = []string{}
			}
			events = append(events, fixtureEvent("event-work-done-completed", "work.transitioned", store.SubjectWorkItem, w.ID, actor, w.TerminalAt, map[string]any{"from": from, "to": to, "reason": "fixture completed", "evidence_refs": evidence, "expected_version": versions[w.ID], "resulting_version": versions[w.ID] + 1}))
			versions[w.ID]++
			continue
		}
		if w.Lifecycle == "in_progress" {
			events = append(events, fixtureEvent("transition-"+w.ID, "work.transitioned", store.SubjectWorkItem, w.ID, "operator", "2026-08-02T09:00:00Z", map[string]any{"from": "needed", "to": "in_progress", "reason": "fixture lifecycle", "expected_version": versions[w.ID], "resulting_version": versions[w.ID] + 1}))
			versions[w.ID]++
		} else if w.Lifecycle == "cancelled" {
			events = append(events, fixtureEvent("transition-"+w.ID, "work.transitioned", store.SubjectWorkItem, w.ID, "operator", w.TerminalAt, map[string]any{"from": "needed", "to": "cancelled", "reason": "fixture lifecycle", "expected_version": versions[w.ID], "resulting_version": versions[w.ID] + 1}))
			versions[w.ID]++
		}
	}
	if len(events) > 0 {
		if err := store.ApplyOperation(ctx, s, store.Operation{Events: events, ExpectedVersions: map[store.SubjectRef]int64{}}); err != nil {
			return fmt.Errorf("pm1fixture: seed work transitions: %w", err)
		}
	}
	for _, r := range c.Fixtures.Relations {
		switch r.Kind {
		case "depends_on":
			event := fixtureEvent("relation-depends", "relation.added", store.SubjectWorkItem, r.Target, "operator", "2026-08-06T00:00:00Z", map[string]any{"from": r.Target, "to": r.Source, "kind": "blocks", "reason": "fixture dependency", "expected_version": versions[r.Target], "resulting_version": versions[r.Target] + 1})
			if err := store.ApplyOperation(ctx, s, store.Operation{Events: []store.Event{event}}); err != nil {
				return fmt.Errorf("pm1fixture: seed depends_on relation %s->%s: %w", r.Source, r.Target, err)
			}
			versions[r.Target]++
		case "supersedes":
			event := fixtureEvent("relation-supersedes", "work.superseded", store.SubjectWorkItem, r.Target, "operator", "2026-08-06T00:00:00Z", map[string]any{"successor": r.Source, "superseded": r.Target, "reason": "fixture supersession", "expected_version": versions[r.Target], "resulting_version": versions[r.Target] + 1})
			if err := store.ApplyOperation(ctx, s, store.Operation{Events: []store.Event{event}}); err != nil {
				return fmt.Errorf("pm1fixture: seed supersedes relation %s->%s: %w", r.Source, r.Target, err)
			}
			versions[r.Target]++
		}
	}
	return nil
}

// SeedKnowledge initializes a throwaway git repository containing the canonical
// work note, lesson, and decision referenced by c, registers it as the
// KnowledgeHome for prod-alpha, and links the completed work item to its
// canonical note. The returned GitKnowledge exposes the resolved aliases used
// by expected-value resolution.
//
// dir must be a caller-owned directory whose lifetime the caller controls, so
// the throwaway repository is reclaimed with it; test callers pass t.TempDir().
func SeedKnowledge(ctx context.Context, s *store.Store, c Corpus, dir string) (GitKnowledge, error) {
	repo, err := initKnowledgeRepo(dir)
	if err != nil {
		return GitKnowledge{}, fmt.Errorf("pm1fixture: init knowledge repo: %w", err)
	}
	workPath := "docs/work/2026-08-03-auth-release.md"
	if err := writeKnowledgeFile(repo, workPath, canonicalWorkNote("work-done", "2026-08-03T12:00:00Z")); err != nil {
		return GitKnowledge{}, fmt.Errorf("pm1fixture: write work note: %w", err)
	}
	lessonPath := "docs/lessons/2026-08-04-state-authority.md"
	decisionPath := "docs/decisions/CD-0002-state-authority.md"
	if err := writeKnowledgeFile(repo, lessonPath, canonicalKnowledgeNote("knowledge-lesson", "lesson", "2026-08-05T12:00:00Z", []string{"state-authority", "sqlite"})); err != nil {
		return GitKnowledge{}, fmt.Errorf("pm1fixture: write lesson note: %w", err)
	}
	if err := writeKnowledgeFile(repo, decisionPath, canonicalKnowledgeNote("knowledge-decision", "decision", "2026-08-04T12:00:00Z", []string{"sqlite", "governance"})); err != nil {
		return GitKnowledge{}, fmt.Errorf("pm1fixture: write decision note: %w", err)
	}
	lessonContent, err := os.ReadFile(filepath.Join(repo, filepath.FromSlash(lessonPath))) //nolint:gosec // repo is a fresh fixture repository and lessonPath is a fixed internal path.
	if err != nil {
		return GitKnowledge{}, fmt.Errorf("pm1fixture: read lesson note: %w", err)
	}
	decisionContent, err := os.ReadFile(filepath.Join(repo, filepath.FromSlash(decisionPath))) //nolint:gosec // repo is a fresh fixture repository and decisionPath is a fixed internal path.
	if err != nil {
		return GitKnowledge{}, fmt.Errorf("pm1fixture: read decision note: %w", err)
	}
	records := []store.KnowledgeRecord{
		manifestRecordFromFile("knowledge-lesson", "lesson", lessonPath, "published", "2026-08-05T12:00:00Z", "Durable lesson", "Governance summary", []string{"state-authority", "sqlite"}, store.KnowledgeRecordScopes{Mode: "home"}, lessonContent),
		manifestRecordFromFile("knowledge-decision", "decision", decisionPath, "accepted", "2026-08-04T12:00:00Z", "Durable decision", "Durable summary", []string{"sqlite", "governance"}, store.KnowledgeRecordScopes{Mode: "home"}, decisionContent),
	}
	if err := writeKnowledgeManifest(repo, records); err != nil {
		return GitKnowledge{}, fmt.Errorf("pm1fixture: write knowledge manifest: %w", err)
	}
	commit, err := commitKnowledgeRepo(repo, "accepted PM1 corpus")
	if err != nil {
		return GitKnowledge{}, fmt.Errorf("pm1fixture: commit knowledge repo: %w", err)
	}
	home := store.KnowledgeHome{HomeProjectID: "proj-web", HomeLocatorID: "repo-alpha-web", RepoPath: repo, HeadRef: "HEAD"}
	if err := authorizeKnowledgeProductHome(ctx, s, "prod-alpha", home); err != nil {
		return GitKnowledge{}, fmt.Errorf("pm1fixture: authorize product home: %w", err)
	}
	if err := s.RebuildKnowledgeIndex(ctx, home); err != nil {
		return GitKnowledge{}, fmt.Errorf("pm1fixture: rebuild knowledge index: %w", err)
	}
	var version int64
	if err := s.DatabaseForTesting().QueryRowContext(ctx, `SELECT version FROM work_items WHERE id = 'work-done'`).Scan(&version); err != nil {
		return GitKnowledge{}, fmt.Errorf("pm1fixture: read work-done version: %w", err)
	}
	if err := store.PublishCompactionLink(ctx, s, store.CompactionLinkRequest{EventID: "corpus-compaction-work-done", WorkID: "work-done", ExpectedVersion: version, Actor: "operator", OccurredAt: time.Date(2026, 8, 7, 0, 0, 0, 0, time.UTC), Home: home, CommitOID: commit, NotePath: workPath, Reason: "accepted corpus fixture"}); err != nil {
		return GitKnowledge{}, fmt.Errorf("pm1fixture: publish compaction link: %w", err)
	}
	if err := s.RebuildKnowledgeIndex(ctx, home); err != nil {
		return GitKnowledge{}, fmt.Errorf("pm1fixture: rebuild knowledge index after compaction: %w", err)
	}
	commitAlias := map[string]string{}
	hashAlias := map[string]string{}
	for _, fixture := range c.Fixtures.Knowledge {
		verified, err := store.VerifyCommittedNote(ctx, repo, commit, fixture.Path, "")
		if err != nil {
			return GitKnowledge{}, fmt.Errorf("pm1fixture: fixture knowledge %q does not resolve to a committed note: %w", fixture.ID, err)
		}
		if existing := commitAlias[fixture.Commit]; existing != "" && existing != commit {
			return GitKnowledge{}, fmt.Errorf("pm1fixture: fixture commit alias %q resolves ambiguously", fixture.Commit)
		}
		if existing := hashAlias[fixture.ContentHash]; existing != "" && existing != verified.ContentHash {
			return GitKnowledge{}, fmt.Errorf("pm1fixture: fixture content hash alias %q resolves ambiguously", fixture.ContentHash)
		}
		commitAlias[fixture.Commit] = commit
		hashAlias[fixture.ContentHash] = verified.ContentHash
	}
	return GitKnowledge{Home: home, CommitAlias: commitAlias, HashAlias: hashAlias}, nil
}

// SeedLaggingKnowledge commits one further indexable decision into an already
// seeded knowledge home and deliberately does not rebuild the SQLite index. The
// projection is then behind the git head by exactly one accepted decision, which
// is the deterministic form of the TS1 `knowledge_index_lagging` fault.
//
// It returns the stable ID of the decision the index cannot yet see. A caller
// asserting honest degradation needs that ID: the omission is only real when
// there is a specific accepted record the answer is missing, so a query that
// silently reported completeness would be making a false claim rather than an
// unlucky one.
func SeedLaggingKnowledge(home store.KnowledgeHome) (string, error) {
	const id = "knowledge-decision-lagging"
	const path = "docs/decisions/CD-0003-lagging-authority.md"
	manifestBody, err := os.ReadFile(filepath.Join(home.RepoPath, filepath.FromSlash("docs/concord-knowledge-index.v1.json")))
	if err != nil {
		return "", fmt.Errorf("pm1fixture: read knowledge manifest: %w", err)
	}
	var manifest store.KnowledgeManifest
	if err := json.Unmarshal(manifestBody, &manifest); err != nil {
		return "", fmt.Errorf("pm1fixture: parse knowledge manifest: %w", err)
	}
	if err := writeKnowledgeFile(home.RepoPath, path, canonicalKnowledgeNote(id, "decision", "2026-08-06T12:00:00Z", []string{"sqlite", "governance"})); err != nil {
		return "", fmt.Errorf("pm1fixture: write lagging decision: %w", err)
	}
	content, err := os.ReadFile(filepath.Join(home.RepoPath, filepath.FromSlash(path)))
	if err != nil {
		return "", fmt.Errorf("pm1fixture: read lagging decision: %w", err)
	}
	records := append(append([]store.KnowledgeRecord{}, manifest.Records...),
		manifestRecordFromFile(id, "decision", path, "accepted", "2026-08-06T12:00:00Z", "Lagging decision", "Unscanned summary", []string{"sqlite", "governance"}, store.KnowledgeRecordScopes{Mode: "home"}, content))
	if err := writeKnowledgeManifest(home.RepoPath, records); err != nil {
		return "", fmt.Errorf("pm1fixture: write extended knowledge manifest: %w", err)
	}
	if _, err := commitKnowledgeRepo(home.RepoPath, "accepted decision the index has not scanned"); err != nil {
		return "", fmt.Errorf("pm1fixture: commit lagging decision: %w", err)
	}
	return id, nil
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

func fixtureEvent(id, kind string, subject store.SubjectType, subjectID, actor, occurred string, payload map[string]any) store.Event {
	raw, err := json.Marshal(payload)
	if err != nil {
		panic(err)
	}
	when, err := time.Parse(time.RFC3339, occurred)
	if err != nil {
		when = time.Date(2026, 8, 7, 12, 0, 0, 0, time.UTC)
	}
	version := 1
	if kind == "work.created" {
		version = 2
	}
	return store.Event{EventID: id, Kind: kind, SubjectType: subject, SubjectID: subjectID, Actor: actor, OccurredAt: when, PayloadVersion: version, Payload: raw}
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

func canonicalKnowledgeNote(id, kind, completed string, tags []string) string {
	tagList := joinTags(tags)
	return "---\n" +
		"id: " + id + "\n" +
		"type: " + kind + "\n" +
		"title: Durable " + kind + "\n" +
		"completed_at: " + completed + "\n" +
		"outcome_tag: published\n" +
		"lesson_tags: [" + tagList + "]\n" +
		"terminal_state: completed\n" +
		"priority: 0\n" +
		"summary: Durable summary\n" +
		"product_ids: [prod-alpha]\n" +
		"project_ids: []\n" +
		"domain_ids: [state]\n" +
		"tag_ids: [" + tagList + "]\n" +
		"---\n\nDurable knowledge.\n"
}

func joinTags(tags []string) string {
	if len(tags) == 0 {
		return ""
	}
	out := tags[0]
	for _, t := range tags[1:] {
		out += ", " + t
	}
	return out
}

func manifestRecordFromFile(id, kind, path, status, date, title, summary string, tags []string, scopes store.KnowledgeRecordScopes, content []byte) store.KnowledgeRecord {
	sum := sha256.Sum256(content)
	scopes.ProductIDs = append([]string{}, scopes.ProductIDs...)
	scopes.ProjectIDs = append([]string{}, scopes.ProjectIDs...)
	scopes.DomainIDs = append([]string{}, scopes.DomainIDs...)
	scopes.TagIDs = append([]string{}, scopes.TagIDs...)
	return store.KnowledgeRecord{
		ID:      id,
		Kind:    kind,
		Path:    path,
		Status:  status,
		Date:    date,
		Title:   title,
		Summary: summary,
		Tags:    append([]string{}, tags...),
		Scopes:  scopes,
		SHA256:  "sha256:" + hex.EncodeToString(sum[:]),
	}
}

func writeKnowledgeManifest(repo string, records []store.KnowledgeRecord) error {
	const productKey = "fixture-product"
	const rootDomainID = "product-root:" + productKey
	domains := []store.KnowledgeDomain{{DomainID: rootDomainID, Name: "Fixture product", Purpose: "Product-wide fixture law", Status: "current", ArchitectureRelations: []store.KnowledgeArchitectureRelation{}}}
	declared := map[string]bool{rootDomainID: true}
	for index := range records {
		records[index].Scopes.ProductIDs = nonNil(records[index].Scopes.ProductIDs)
		records[index].Scopes.ProjectIDs = nonNil(records[index].Scopes.ProjectIDs)
		records[index].Scopes.DomainIDs = nonNil(records[index].Scopes.DomainIDs)
		records[index].Scopes.TagIDs = nonNil(records[index].Scopes.TagIDs)
		for _, domainID := range records[index].Scopes.DomainIDs {
			if !declared[domainID] {
				declared[domainID] = true
				domains = append(domains, store.KnowledgeDomain{DomainID: domainID, Name: domainID, Purpose: "Fixture scope Domain", ParentDomainID: rootDomainID, Status: "current", ArchitectureRelations: []store.KnowledgeArchitectureRelation{}})
			}
		}
		if records[index].Status == "accepted" && (records[index].Kind == "decision" || records[index].Kind == "spec" || records[index].Kind == "constitution") {
			records[index].HomeDomainID = rootDomainID
			records[index].ProductWideRationale = "Fixture law binds every child Domain."
		}
	}
	manifest := store.KnowledgeManifest{
		SchemaVersion:  "1.2",
		SupportedKinds: []string{"work_note", "decision", "spec", "lesson", "research"},
		IndexedKinds:   []string{"work_note", "decision", "spec", "lesson"},
		DomainRegistry: store.KnowledgeDomainRegistry{SchemaVersion: "1.0", ProductKey: productKey, RootDomainID: rootDomainID, Domains: domains},
		Records:        records,
	}
	body, err := json.MarshalIndent(manifest, "", "  ")
	if err != nil {
		return err
	}
	return writeKnowledgeFile(repo, "docs/concord-knowledge-index.v1.json", string(body)+"\n")
}

func nonNil(values []string) []string {
	if values == nil {
		return []string{}
	}
	return values
}

func authorizeKnowledgeLocator(ctx context.Context, s *store.Store, home store.KnowledgeHome) (err error) {
	if _, err := s.DatabaseForTesting().ExecContext(ctx, `INSERT INTO fold_guard(active) VALUES(1)`); err != nil {
		return err
	}
	defer func() {
		_, cleanupErr := s.DatabaseForTesting().ExecContext(ctx, `DELETE FROM fold_guard`)
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
	if _, err := s.DatabaseForTesting().ExecContext(ctx, `INSERT INTO fold_guard(active) VALUES(1)`); err != nil {
		return err
	}
	defer func() {
		_, cleanupErr := s.DatabaseForTesting().ExecContext(ctx, `DELETE FROM fold_guard`)
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
	if _, err := s.DatabaseForTesting().ExecContext(ctx, `DELETE FROM fold_guard`); err != nil {
		return err
	}
	return nil
}
