package store

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// KnowledgePublicationRecord describes commit publication, not law or proof validity.
type KnowledgePublicationRecord struct {
	ProjectID         string `json:"project_id"`
	LocatorID         string `json:"locator_id"`
	ServedCommit      string `json:"served_commit,omitempty"`
	OriginIdentity    string `json:"origin_identity,omitempty"`
	PublicationRef    string `json:"publication_ref,omitempty"`
	PublicationCommit string `json:"publication_commit,omitempty"`
	ObservedAt        string `json:"observed_at,omitempty"`
	Basis             string `json:"basis"`
	Comparison        string `json:"comparison"`
	Cause             string `json:"cause,omitempty"`
	Recovery          string `json:"recovery,omitempty"`
}

// KnowledgePublication keeps a failed latest check separate from past success.
type KnowledgePublication struct {
	KnowledgePublicationRecord
	PreviousSuccess *KnowledgePublicationRecord `json:"previous_success,omitempty"`
}

const (
	publicationRecovery             = "explicitly observe this designated knowledge home again"
	publicationSourceRecovery       = "ask the home owner to verify the designation, local repository access, and served revision through the existing configuration route; then explicitly observe this home again"
	publicationOriginRecovery       = "ask the source owner to configure exactly one intended origin fetch URL through the existing source configuration route; then explicitly observe this source again"
	publicationAccessRecovery       = "ask the source owner to check origin connectivity, configured transport, and existing credentials through the authorized access route; repair the observed failure before explicitly observing this source again"
	publicationDefaultRecovery      = "ask the source owner to repair origin's advertised default branch and fully qualified branch reference through the existing repository configuration route; then explicitly observe this source again"
	publicationAncestryRecovery     = "ask the source owner to verify and restore the required local commit ancestry through the existing authorized repository-maintenance route, or use an already authorized suitable source; then repeat the explicit observation and knowledge read"
	publicationRaceRecovery         = "ask the source owner to resolve competing source or tracking-ref changes and verify source configuration and local ref-write access; then repeat the explicit observation and knowledge read"
	publicationLagRecovery          = "ask the source owner to use its existing safe update route, or use an already authorized suitable source; then repeat the explicit publication observation and knowledge read"
	publicationDeadlineRecovery     = "ask the source owner to check access and configured transport delays through the authorized access route; resolve the deadline failure before explicitly observing this source again"
	publicationCancellationRecovery = "if observation is still wanted, resolve the caller cancellation before explicitly observing the designated home again"
)

var (
	errPublicationOriginConfiguration = errors.New("origin must have exactly one bounded fetch URL")
	errPublicationShallowAncestry     = errors.New("local commit ancestry is shallow")
)

var knowledgePublicationTimeout = 10 * time.Second

type publicationSource struct {
	Home    KnowledgeHome
	Binding string
}

func (source *publicationSource) resolve(ctx context.Context, q queryer, productID, projectID, locatorID string) error {
	var err error
	*source, err = publicationSourceSnapshot(ctx, q, productID, projectID, locatorID)
	return err
}

func publicationDigest(value []byte) string {
	sum := sha256.Sum256(value)
	return "sha256:" + hex.EncodeToString(sum[:])
}

// The binding includes the full source set and Product version, so removing and
// re-registering a source cannot silently reuse an earlier observation.
func publicationSourceSnapshot(ctx context.Context, q queryer, productID, projectID, locatorID string) (publicationSource, error) {
	sources, err := resolveKnowledgeQuerySources(ctx, q, productID, "knowledge_publication")
	if err != nil {
		return publicationSource{}, err
	}
	var selected KnowledgeHome
	for _, home := range sources {
		if home.HomeProjectID == projectID && home.HomeLocatorID == locatorID {
			selected = home
		}
	}
	if selected.RepoPath == "" || len(projectID) > 128 || len(locatorID) > 128 {
		return publicationSource{}, newFailure(KindUnknownScope, "knowledge_publication", "selector is not the exact designated Product knowledge home", false, "select the designated Project and locator pair")
	}
	var version int64
	if err := q.QueryRowContext(ctx, `SELECT version FROM products WHERE id=?`, productID).Scan(&version); err != nil {
		return publicationSource{}, err
	}
	encoded, err := json.Marshal(struct {
		Sources []KnowledgeHome
		Version int64
	}{sources, version})
	if err != nil {
		return publicationSource{}, err
	}
	return publicationSource{Home: selected, Binding: publicationDigest(encoded)}, nil
}

func publicationUnknown(home KnowledgeHome, served, basis, cause string) KnowledgePublication {
	recovery := publicationRecovery
	if basis == "explicit_remote_observation" {
		recovery = publicationSourceRecovery
	}
	return KnowledgePublication{KnowledgePublicationRecord: KnowledgePublicationRecord{ProjectID: home.HomeProjectID, LocatorID: home.HomeLocatorID, ServedCommit: served, Basis: basis, Comparison: "unknown", Cause: cause, Recovery: recovery}}
}

// Only the observed stage, bounded exit status, and context termination enter
// the wire record. Git stderr and deeper transport diagnoses are not evidence.
func publicationGitFailure(result *KnowledgePublication, ctx context.Context, stage string, err error, recovery string) {
	result.Comparison, result.Recovery = "unknown", recovery
	switch {
	case errors.Is(ctx.Err(), context.DeadlineExceeded) || errors.Is(err, context.DeadlineExceeded):
		result.Cause = stage + " exceeded the observation deadline; underlying cause unknown"
		result.Recovery = publicationDeadlineRecovery
	case errors.Is(ctx.Err(), context.Canceled) || errors.Is(err, context.Canceled):
		result.Cause = stage + " canceled; underlying cause unknown"
		result.Recovery = publicationCancellationRecovery
	default:
		var exit interface{ ExitCode() int }
		if errors.As(err, &exit) && exit.ExitCode() >= 0 && exit.ExitCode() <= 255 {
			result.Cause = stage + " failed with exit status " + strconv.Itoa(exit.ExitCode()) + "; underlying cause unknown"
		} else {
			result.Cause = stage + " failed; exit status unavailable; underlying cause unknown"
		}
	}
}

// publicationOrigin hashes the exact single configured fetch URL, not a
// credential-bearing URL or an expansion of Git's transport configuration.
func publicationOrigin(ctx context.Context, runner GitRunner, home KnowledgeHome) (string, error) {
	raw, err := runner.Run(ctx, home.RepoPath, "config", "--null", "--get-all", "remote.origin.url")
	if err != nil {
		return "", err
	}
	if len(raw) < 2 || len(raw) > 8193 || raw[len(raw)-1] != 0 || strings.Count(string(raw), "\x00") != 1 {
		return "", errPublicationOriginConfiguration
	}
	return publicationDigest(raw[:len(raw)-1]), nil
}

// ObserveKnowledgePublication is the only network entry point. Ordinary reads
// consume its retained evidence and never refresh origin.
func (s *Store) ObserveKnowledgePublication(ctx context.Context, productID, projectID, locatorID string) (KnowledgePublication, error) {
	return s.observeKnowledgePublication(ctx, productID, projectID, locatorID, ExecGitRunner{})
}

func (s *Store) observeKnowledgePublication(ctx context.Context, productID, projectID, locatorID string, runner FreshnessRunner) (KnowledgePublication, error) {
	if s == nil || s.db == nil {
		return KnowledgePublication{}, newFailure(KindUnavailable, "knowledge_publication", "store is not open", false, "open a store before observing publication")
	}
	var source publicationSource
	err := s.readSnapshot(ctx, func(q queryer) error {
		return source.resolve(ctx, q, productID, projectID, locatorID)
	})
	if err != nil {
		return KnowledgePublication{}, err
	}
	probeCtx, cancel := context.WithTimeout(ctx, knowledgePublicationTimeout)
	defer cancel()
	result, witness := observePublicationGit(probeCtx, publicationLocalRunner{runner}, source.Home)
	result.ObservedAt = s.now().Format(time.RFC3339Nano)
	if probeCtx.Err() != nil && result.Comparison != "unknown" {
		publicationGitFailure(&result, probeCtx, "publication observation", probeCtx.Err(), publicationSourceRecovery)
	}
	if ctx.Err() != nil {
		return result, ctx.Err()
	}
	return s.persistPublication(ctx, productID, source, result, witness)
}

// Local object probes must not turn missing promisor objects into extra network
// operations. This wraps the existing bounded runner without changing it.
type publicationLocalRunner struct{ FreshnessRunner }

func (r publicationLocalRunner) Run(ctx context.Context, repo string, args ...string) ([]byte, error) {
	return r.FreshnessRunner.Run(ctx, repo, append([]string{"--no-lazy-fetch"}, args...)...)
}

// Configuration files and HEAD are read again without subprocesses inside the
// short persistence transaction. Include files come from Git's effective config
// listing; their paths and bytes never enter the cache or wire response.
type publicationWitness map[string]string

func publicationFiles(ctx context.Context, runner GitRunner, home KnowledgeHome) (publicationWitness, error) {
	raw, err := runner.Run(ctx, home.RepoPath, "config", "--null", "--show-origin", "--list")
	if err != nil {
		return nil, err
	}
	parts := strings.Split(string(raw), "\x00")
	if len(parts)%2 != 1 || parts[len(parts)-1] != "" {
		return nil, errors.New("invalid configuration listing")
	}
	gitDir, err := knowledgeGitDir(home.RepoPath)
	if err != nil {
		return nil, err
	}
	files := publicationWitness{filepath.Join(gitDir, "HEAD"): ""}
	for i := 0; i+1 < len(parts); i += 2 {
		if name, ok := strings.CutPrefix(parts[i], "file:"); ok {
			if !filepath.IsAbs(name) {
				name = filepath.Join(home.RepoPath, name)
			}
			files[name] = ""
		}
	}
	if len(files) > 16 {
		return nil, errors.New("configuration witness exceeds its file bound")
	}
	for name := range files {
		digest, err := publicationFileDigest(name)
		if err != nil {
			return nil, errors.New("configuration witness unavailable")
		}
		files[name] = digest
	}
	return files, nil
}

func (w publicationWitness) unchanged() bool {
	if len(w) == 0 {
		return false
	}
	for name, digest := range w {
		current, err := publicationFileDigest(name)
		if err != nil || current != digest {
			return false
		}
	}
	return true
}

func publicationFileDigest(name string) (digest string, err error) {
	file, err := os.Open(name) //nolint:gosec // private local Git configuration/HEAD witnesses, never served content.
	if err != nil {
		return "", err
	}
	defer func() { err = errors.Join(err, file.Close()) }()
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Size() > 64*1024 {
		return "", errors.New("configuration witness is not a bounded regular file")
	}
	body, err := io.ReadAll(io.LimitReader(file, 64*1024+1))
	if err != nil || len(body) > 64*1024 {
		return "", errors.New("configuration witness read failed")
	}
	return publicationDigest(body), nil
}

func observePublicationGit(ctx context.Context, runner FreshnessRunner, home KnowledgeHome) (KnowledgePublication, publicationWitness) {
	result := publicationUnknown(home, "", "explicit_remote_observation", "local source probe failed")
	head, err := resolveCommitSHARunner(ctx, runner, home.RepoPath, home.HeadRef)
	if err != nil {
		publicationGitFailure(&result, ctx, "served commit probe", err, publicationSourceRecovery)
		return result, nil
	}
	if validateCommitOID(head) != nil {
		result.Cause = "served commit probe returned an invalid commit identity"
		return result, nil
	}
	result.ServedCommit = head
	origin, err := publicationOrigin(ctx, runner, home)
	if err != nil {
		if errors.Is(err, errPublicationOriginConfiguration) || publicationExitOne(err) {
			result.Cause, result.Recovery = "origin fetch URL is missing or ambiguous", publicationOriginRecovery
		} else {
			publicationGitFailure(&result, ctx, "origin configuration probe", err, publicationOriginRecovery)
		}
		return result, nil
	}
	result.OriginIdentity = origin
	witness, err := publicationFiles(ctx, runner, home)
	if err != nil {
		publicationGitFailure(&result, ctx, "local configuration witness", err, publicationSourceRecovery)
		return result, nil
	}
	raw, err := runner.RunNoninteractive(ctx, home.RepoPath, "ls-remote", "--symref", "origin", "HEAD")
	if err != nil {
		publicationGitFailure(&result, ctx, "origin default branch discovery", err, publicationAccessRecovery)
		return result, witness
	}
	ref, commit, ok := publicationAdvertisement(raw)
	if !ok {
		result.Cause, result.Recovery = "origin default branch advertisement is missing or ambiguous", publicationDefaultRecovery
		return result, witness
	}
	if _, err := runner.Run(ctx, home.RepoPath, "check-ref-format", ref); err != nil {
		publicationGitFailure(&result, ctx, "origin default branch reference validation", err, publicationDefaultRecovery)
		return result, witness
	}
	result.PublicationRef, result.PublicationCommit = ref, commit
	tracking := "refs/remotes/origin/" + strings.TrimPrefix(ref, "refs/heads/")
	if _, err := runner.Run(ctx, home.RepoPath, "symbolic-ref", "--quiet", tracking); err == nil {
		result.Cause, result.Recovery = "selected origin tracking reference is symbolic", publicationRaceRecovery
		return result, witness
	} else if !publicationExitOne(err) {
		publicationGitFailure(&result, ctx, "tracking reference type probe", err, publicationRaceRecovery)
		return result, witness
	}
	oldRaw, err := runner.Run(ctx, home.RepoPath, "rev-parse", "--verify", "--quiet", tracking)
	old := strings.TrimSpace(string(oldRaw))
	switch {
	case publicationExitOne(err):
		old = strings.Repeat("0", len(commit))
	case err != nil:
		publicationGitFailure(&result, ctx, "tracking reference value probe", err, publicationRaceRecovery)
		return result, witness
	case validateCommitOID(old) != nil:
		result.Cause, result.Recovery = "tracking reference value probe returned an invalid object identity", publicationRaceRecovery
		return result, witness
	}
	// Fetch objects and FETCH_HEAD only. An explicit empty refmap ignores the
	// configured destinations, so fetch cannot follow a raced symbolic ref.
	_, err = runner.RunNoninteractive(ctx, home.RepoPath, "fetch", "--no-tags", "--no-recurse-submodules", "--no-prune", "--no-auto-maintenance", "--refmap=", "origin", ref)
	if err != nil {
		publicationGitFailure(&result, ctx, "origin selected branch fetch", err, publicationAccessRecovery)
		return result, witness
	}
	fetched, err := resolveCommitSHARunner(ctx, runner, home.RepoPath, "FETCH_HEAD")
	if err != nil {
		publicationGitFailure(&result, ctx, "fetched commit verification", err, publicationAncestryRecovery)
		return result, witness
	}
	if fetched != commit {
		result.Cause, result.Recovery = "fetched commit does not match discovery; concurrent change or underlying cause unknown", publicationRaceRecovery
		return result, witness
	}
	current, err := resolveCommitSHARunner(ctx, runner, home.RepoPath, home.HeadRef)
	if err != nil {
		publicationGitFailure(&result, ctx, "served commit revalidation", err, publicationRaceRecovery)
		return result, witness
	}
	currentOrigin, originErr := publicationOrigin(ctx, runner, home)
	if originErr != nil {
		publicationGitFailure(&result, ctx, "origin configuration revalidation", originErr, publicationOriginRecovery)
		return result, witness
	}
	if current != head || currentOrigin != origin || !witness.unchanged() {
		result.Cause, result.Recovery = "source provenance changed or could not be revalidated during observation", publicationRaceRecovery
		return result, witness
	}
	// Git locks and compares the selected ref, then replaces that ref itself.
	// --no-deref protects every referent even if another process replaces the
	// sampled direct ref with a symbolic ref immediately before this command.
	if _, err := runner.Run(ctx, home.RepoPath, "update-ref", "--no-deref", tracking, commit, old); err != nil {
		publicationGitFailure(&result, ctx, "tracking reference compare-and-swap", err, publicationRaceRecovery)
		return result, witness
	}
	result.Comparison, err = comparePublicationCommits(ctx, runner, home.RepoPath, head, commit)
	if errors.Is(err, errPublicationShallowAncestry) {
		result.Cause, result.Recovery = "local commit ancestry is shallow; publication comparison unknown", publicationAncestryRecovery
		return result, witness
	}
	if err != nil {
		publicationGitFailure(&result, ctx, "local ancestry probe", err, publicationAncestryRecovery)
		return result, witness
	}
	result.Cause, result.Recovery = "", ""
	if result.Comparison == "lagging" {
		result.Recovery = publicationLagRecovery
	}
	return result, witness
}

func publicationAdvertisement(raw []byte) (string, string, bool) {
	var ref, commit string
	for _, line := range strings.Split(strings.TrimSuffix(string(raw), "\n"), "\n") {
		value, target, ok := strings.Cut(line, "\t")
		if !ok || target != "HEAD" {
			return "", "", false
		}
		if name, symbolic := strings.CutPrefix(value, "ref: "); symbolic {
			if ref != "" || !strings.HasPrefix(name, "refs/heads/") || len(name) > 1024 {
				return "", "", false
			}
			ref = name
		} else {
			if commit != "" || validateCommitOID(value) != nil {
				return "", "", false
			}
			commit = value
		}
	}
	return ref, commit, ref != "" && commit != ""
}

func comparePublicationCommits(ctx context.Context, runner GitRunner, repo, served, published string) (string, error) {
	if served == published {
		return "match", nil
	}
	shallow, err := runner.Run(ctx, repo, "rev-parse", "--is-shallow-repository")
	if err != nil {
		return "unknown", err
	}
	if strings.TrimSpace(string(shallow)) != "false" {
		return "unknown", errPublicationShallowAncestry
	}
	// A merge-base probe must distinguish exit 1 (not an ancestor) from missing
	// objects and other failures. rev-list proves the complete union first.
	if _, err := runner.Run(ctx, repo, "rev-list", "--count", "--missing=error", served, published, "--"); err != nil {
		return "unknown", err
	}
	ancestor := func(a, b string) (bool, error) {
		_, err := runner.Run(ctx, repo, "merge-base", "--is-ancestor", a, b)
		if err == nil {
			return true, nil
		}
		if publicationExitOne(err) {
			return false, nil
		}
		return false, err
	}
	if yes, err := ancestor(served, published); yes {
		return "lagging", nil
	} else if err != nil {
		return "unknown", err
	}
	if yes, err := ancestor(published, served); yes {
		return "unpublished", nil
	} else if err != nil {
		return "unknown", err
	}
	return "diverged", nil
}

func publicationExitOne(err error) bool {
	var exit interface{ ExitCode() int }
	return errors.As(err, &exit) && exit.ExitCode() == 1
}

func (s *Store) persistPublication(ctx context.Context, productID string, source publicationSource, result KnowledgePublication, witness publicationWitness) (KnowledgePublication, error) {
	tx, err := beginOrdinaryTx(ctx, s.db)
	if err != nil {
		return result, err
	}
	defer tx.Rollback()
	current, sourceErr := publicationSourceSnapshot(ctx, tx, productID, source.Home.HomeProjectID, source.Home.HomeLocatorID)
	if sourceErr != nil {
		result.Comparison, result.Cause, result.Recovery = "unknown", "knowledge source membership changed during observation", publicationRaceRecovery
		return result, newFailure(KindStaleContext, "knowledge_publication", "knowledge source membership changed during observation", true, publicationRaceRecovery)
	}
	head, headErr := resolveKnowledgeHeadCheap(source.Home.RepoPath, source.Home.HeadRef)
	if current.Binding != source.Binding || current.Home != source.Home || (result.Comparison != "unknown" && (headErr != nil || head != result.ServedCommit || !witness.unchanged())) {
		result.Comparison, result.Cause, result.Recovery = "unknown", "source provenance changed during observation", publicationRaceRecovery
	}
	var stored, success string
	if err := tx.QueryRowContext(ctx, `SELECT latest_record,success_record FROM knowledge_publication_observations WHERE project_id=? AND locator_id=?`, source.Home.HomeProjectID, source.Home.HomeLocatorID).Scan(&stored, &success); err != nil && !errors.Is(err, sql.ErrNoRows) {
		return result, err
	}
	latest, err := json.Marshal(result.KnowledgePublicationRecord)
	if err != nil {
		return result, err
	}
	if result.Comparison != "unknown" {
		success = string(latest)
	} else if success != "" {
		result.PreviousSuccess = &KnowledgePublicationRecord{}
		if err := json.Unmarshal([]byte(success), result.PreviousSuccess); err != nil {
			return result, err
		}
	}
	// A concurrent observation that finished later stays the latest evidence;
	// an older success committed afterwards must not replace a newer failure.
	if publicationObservedLater(stored, result.ObservedAt) {
		return result, nil
	}
	if err := enterWrite(ctx, tx); err != nil {
		return result, err
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO knowledge_publication_observations(project_id,locator_id,source_binding,latest_record,success_record) VALUES(?,?,?,?,?) ON CONFLICT(project_id,locator_id) DO UPDATE SET source_binding=excluded.source_binding,latest_record=excluded.latest_record,success_record=excluded.success_record`, source.Home.HomeProjectID, source.Home.HomeLocatorID, source.Binding, string(latest), success)
	if err != nil {
		return result, err
	}
	if err := leaveWrite(ctx, tx); err != nil {
		return result, err
	}
	return result, tx.Commit()
}

func publicationObservedLater(stored, observedAt string) bool {
	var record KnowledgePublicationRecord
	if stored == "" || json.Unmarshal([]byte(stored), &record) != nil {
		return false
	}
	prior, priorErr := time.Parse(time.RFC3339Nano, record.ObservedAt)
	current, currentErr := time.Parse(time.RFC3339Nano, observedAt)
	return priorErr == nil && currentErr == nil && prior.After(current)
}

func readPublicationCache(ctx context.Context, q queryer, home KnowledgeHome) (KnowledgePublication, string, error) {
	var latest, success, binding string
	err := q.QueryRowContext(ctx, `SELECT latest_record,success_record,source_binding FROM knowledge_publication_observations WHERE project_id=? AND locator_id=?`, home.HomeProjectID, home.HomeLocatorID).Scan(&latest, &success, &binding)
	if err != nil {
		return KnowledgePublication{}, "", err
	}
	var result KnowledgePublication
	if err := json.Unmarshal([]byte(latest), &result.KnowledgePublicationRecord); err != nil {
		return result, "", err
	}
	if result.Comparison == "unknown" && success != "" {
		result.PreviousSuccess = &KnowledgePublicationRecord{}
		if err := json.Unmarshal([]byte(success), result.PreviousSuccess); err != nil {
			return result, "", err
		}
	}
	return result, binding, nil
}

// KnowledgePublicationForRead performs local provenance checks only. Historical
// proofs remain labelled historical even when a matching observation exists.
func (s *Store) KnowledgePublicationForRead(ctx context.Context, home KnowledgeHome, servedCommit string, historical bool) (KnowledgePublication, error) {
	if s == nil || s.db == nil {
		return KnowledgePublication{}, newFailure(KindUnavailable, "knowledge_publication", "store is not open", false, "open a store before reading publication")
	}
	if home.HomeProjectID == "" || home.HomeLocatorID == "" || len(home.HomeProjectID) > 128 || len(home.HomeLocatorID) > 128 {
		return KnowledgePublication{}, newFailure(KindInvalidFilter, "knowledge_publication", "knowledge source IDs must be present and bounded", false, "supply the resolved knowledge source")
	}
	if servedCommit != "" {
		if err := validateCommitOID(servedCommit); err != nil {
			return KnowledgePublication{}, err
		}
	}
	if historical {
		return publicationUnknown(home, servedCommit, "historical_read", "explicit historical proof is local"), nil
	}
	result, binding, err := readPublicationCache(ctx, s.db, home)
	if errors.Is(err, sql.ErrNoRows) {
		return publicationUnknown(home, servedCommit, "not_observed", "publication has not been observed"), nil
	}
	if err != nil {
		return KnowledgePublication{}, err
	}
	result.Basis = "cached_remote_observation"
	unknown := func(cause, recovery string) (KnowledgePublication, error) {
		if result.Comparison != "unknown" {
			previous := result.KnowledgePublicationRecord
			result.PreviousSuccess = &previous
		}
		result.Comparison, result.ServedCommit = "unknown", servedCommit
		result.Cause, result.Recovery = cause, recovery
		return result, nil
	}
	productID, _, err := resolveKnowledgeSourceRole(ctx, s.db, home)
	if err != nil {
		return KnowledgePublication{}, err
	}
	if productID == "" {
		return unknown("knowledge home is no longer designated for publication observation", publicationSourceRecovery)
	}
	source, err := publicationSourceSnapshot(ctx, s.db, productID, home.HomeProjectID, home.HomeLocatorID)
	if err != nil {
		var failure *Failure
		if errors.As(err, &failure) && (failure.Kind == KindUnknownScope || failure.Kind == KindAmbiguousScope) {
			return unknown("designated knowledge home no longer resolves unambiguously", publicationSourceRecovery)
		}
		return KnowledgePublication{}, err
	}
	if source.Binding != binding || source.Home != home {
		return unknown("designated home provenance changed since observation", publicationSourceRecovery)
	}
	if result.ServedCommit != servedCommit {
		return unknown("served commit differs from the observed source revision", publicationSourceRecovery)
	}
	probeCtx, cancel := context.WithTimeout(ctx, gitCommandTimeout)
	defer cancel()
	origin, err := publicationOrigin(probeCtx, publicationLocalRunner{ExecGitRunner{}}, home)
	if err != nil {
		if errors.Is(err, errPublicationOriginConfiguration) || publicationExitOne(err) {
			return unknown("local origin fetch URL is missing or ambiguous; cached publication cannot be revalidated", publicationOriginRecovery)
		}
		failure := result
		publicationGitFailure(&failure, probeCtx, "cached origin configuration probe", err, publicationSourceRecovery)
		return unknown(failure.Cause, failure.Recovery)
	}
	if origin != result.OriginIdentity {
		return unknown("configured origin identity changed since observation", publicationOriginRecovery)
	}
	head, err := resolveKnowledgeHeadCheap(home.RepoPath, home.HeadRef)
	if err != nil {
		return unknown("local served revision could not be resolved; cached publication cannot be revalidated", publicationSourceRecovery)
	}
	if head != servedCommit {
		return unknown("local source revision changed since observation", publicationSourceRecovery)
	}
	return result, nil
}
