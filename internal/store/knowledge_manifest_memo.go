package store

import (
	"context"
	"strconv"
	"sync"
)

// knowledgeManifestMemo remembers composed manifests by the immutable
// commit they were read at, keyed by repository path, commit, and role.
// Git commit identity is content addressing: one commit OID resolves to one
// tree, so a parsed manifest for that OID is the same value a fresh read
// returns, and the memo is result reuse of the single verification owner —
// not a second verifier. Failures and missing manifests are never cached.
// The FIFO bound keeps memory tied to a fixed number of manifests, each
// already bounded by the manifest record cap; commits never expire from the
// tail, so a long-lived store cannot grow the entry set past the bound.
type knowledgeManifestMemo struct {
	mu      sync.RWMutex
	entries map[string]KnowledgeManifest
	order   []string
}

// knowledgeManifestMemoBound caps memoized manifests per store. Sources in a
// federated Product set plus a few distinct historical commits cover the
// working set of repeated Q10 reads; older commits evict first-in-first-out.
const knowledgeManifestMemoBound = 8

func (m *knowledgeManifestMemo) get(key string) (KnowledgeManifest, bool) {
	if m == nil {
		return KnowledgeManifest{}, false
	}
	m.mu.RLock()
	defer m.mu.RUnlock()
	manifest, ok := m.entries[key]
	return manifest, ok
}

func (m *knowledgeManifestMemo) put(key string, manifest KnowledgeManifest) {
	if m == nil {
		return
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.entries == nil {
		m.entries = make(map[string]KnowledgeManifest, knowledgeManifestMemoBound)
	}
	if _, present := m.entries[key]; present {
		return
	}
	m.entries[key] = manifest
	m.order = append(m.order, key)
	for len(m.order) > knowledgeManifestMemoBound {
		oldest := m.order[0]
		m.order = m.order[1:]
		delete(m.entries, oldest)
	}
}

// readKnowledgeManifestCached serves the manifest reader the store passes to
// Q10's historical proof: the memo first, then the existing read owner. The
// per-record declaration and blob proof still verify against git on every
// read, so reachability and tamper evidence stay live.
func (s *Store) readKnowledgeManifestCached(ctx context.Context, repo, commit string, role knowledgeManifestRole) (KnowledgeManifest, bool, error) {
	if s == nil {
		return readKnowledgeManifest(ctx, repo, commit, role)
	}
	s.manifestMemoOnce.Do(func() {
		s.manifestMemo = &knowledgeManifestMemo{}
	})
	key := repo + "\x00" + commit + "\x00" + strconv.Itoa(int(role))
	if manifest, ok := s.manifestMemo.get(key); ok {
		return manifest, false, nil
	}
	manifest, missing, err := readKnowledgeManifest(ctx, repo, commit, role)
	if err == nil && !missing {
		s.manifestMemo.put(key, manifest)
	}
	return manifest, missing, err
}
