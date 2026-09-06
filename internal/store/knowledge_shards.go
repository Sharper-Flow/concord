package store

import (
	"archive/tar"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path"
	"sort"
	"strings"
)

// The knowledge manifest is composed from shards under docs/knowledge/
// (CD-0114). The head shard carries the root fields, the domain registry is
// its own shard, and every record is one file under the record tree. No
// committed file lists every record, so two commits that each add a record
// never touch the same line.
const (
	knowledgeShardRoot     = "docs/knowledge"
	knowledgeHeadPath      = "docs/knowledge/manifest.json"
	knowledgeRegistryPath  = "docs/knowledge/domain-registry.json"
	knowledgeRecordTree    = "docs/knowledge/records"
	knowledgeRecordTreeDir = knowledgeRecordTree + "/"
)

// knowledgeShards is the raw material of one manifest: the head, the domain
// registry, and every record shard keyed by its file name.
type knowledgeShards struct {
	head     []byte
	registry []byte
	records  map[string][]byte
}

// composeKnowledgeManifest assembles the manifest document the shards
// describe and parses it under the same strict rules as an authored
// aggregate. Records are ordered by shard file name, which the generator
// pins to the record id.
func composeKnowledgeManifest(shards knowledgeShards) (KnowledgeManifest, error) {
	if len(shards.head) == 0 {
		return KnowledgeManifest{}, newFailure(KindInvalidNoteProof, "compose_knowledge_manifest", "knowledge manifest head is empty", false, "commit "+knowledgeHeadPath)
	}
	if err := rejectDuplicateJSONKeys(shards.head); err != nil {
		return KnowledgeManifest{}, newFailure(KindInvalidNoteProof, "compose_knowledge_manifest", "knowledge manifest head contains duplicate JSON keys", false, "remove duplicate keys from "+knowledgeHeadPath)
	}
	var head map[string]json.RawMessage
	if err := json.Unmarshal(shards.head, &head); err != nil {
		return KnowledgeManifest{}, wrapFailure(KindInvalidNoteProof, "compose_knowledge_manifest", "knowledge manifest head is not a JSON object", false, "repair "+knowledgeHeadPath, err)
	}
	for _, reserved := range []string{"domain_registry", "records"} {
		if _, present := head[reserved]; present {
			return KnowledgeManifest{}, newFailure(KindInvalidNoteProof, "compose_knowledge_manifest", "knowledge manifest head carries a composed field: "+reserved, false, "move "+reserved+" to its shard")
		}
	}
	if len(shards.registry) != 0 {
		if err := rejectDuplicateJSONKeys(shards.registry); err != nil {
			return KnowledgeManifest{}, newFailure(KindInvalidNoteProof, "compose_knowledge_manifest", "domain registry shard contains duplicate JSON keys", false, "remove duplicate keys from "+knowledgeRegistryPath)
		}
		if !json.Valid(shards.registry) {
			return KnowledgeManifest{}, newFailure(KindInvalidNoteProof, "compose_knowledge_manifest", "domain registry shard is not valid JSON", false, "repair "+knowledgeRegistryPath)
		}
		head["domain_registry"] = json.RawMessage(bytes.TrimSpace(shards.registry))
	}
	if len(shards.records) > maxManifestRecords {
		return KnowledgeManifest{}, newFailure(KindInvalidNoteProof, "compose_knowledge_manifest", "knowledge record shards exceed the bounded record count", false, "publish at most 1000 records")
	}
	names := make([]string, 0, len(shards.records))
	for name := range shards.records {
		names = append(names, name)
	}
	sort.Strings(names)
	records := make([]json.RawMessage, 0, len(names))
	for _, name := range names {
		shard := shards.records[name]
		if err := rejectDuplicateJSONKeys(shard); err != nil {
			return KnowledgeManifest{}, newFailure(KindInvalidNoteProof, "compose_knowledge_manifest", "knowledge record shard contains duplicate JSON keys: "+name, false, "remove duplicate keys from the record shard")
		}
		if !json.Valid(shard) {
			return KnowledgeManifest{}, newFailure(KindInvalidNoteProof, "compose_knowledge_manifest", "knowledge record shard is not valid JSON: "+name, false, "repair the record shard")
		}
		records = append(records, json.RawMessage(bytes.TrimSpace(shard)))
	}
	encodedRecords, err := json.Marshal(records)
	if err != nil {
		return KnowledgeManifest{}, wrapFailure(KindInvalidNoteProof, "compose_knowledge_manifest", "cannot encode the knowledge record list", false, "repair the record shards", err)
	}
	head["records"] = encodedRecords
	composed, err := json.Marshal(head)
	if err != nil {
		return KnowledgeManifest{}, wrapFailure(KindInvalidNoteProof, "compose_knowledge_manifest", "cannot encode the composed knowledge manifest", false, "repair the manifest shards", err)
	}
	return parseKnowledgeManifest(composed)
}

// readKnowledgeShardsAtCommit reads the shard tree at one commit through a
// single git archive call. The second result is false when the commit carries
// no head shard, which is the shape a commit that predates the shards has.
func readKnowledgeShardsAtCommit(ctx context.Context, repo, commit string) (knowledgeShards, bool, error) {
	out, err := runGit(ctx, repo, "ls-tree", "-z", commit, "--", knowledgeHeadPath)
	if err != nil {
		return knowledgeShards{}, false, wrapFailure(KindGitUnreachable, "read_knowledge_manifest", "cannot inspect the knowledge shard tree", true, "restore the git object and retry", err)
	}
	entries, err := parseTreeEntries(out)
	if err != nil {
		return knowledgeShards{}, false, wrapFailure(KindInvalidNoteProof, "read_knowledge_manifest", "manifest tree entry is malformed", false, "repair the canonical git tree", err)
	}
	if len(entries) == 0 {
		return knowledgeShards{}, false, nil
	}
	if entries[0].kind != "blob" || entries[0].mode != "100644" {
		return knowledgeShards{}, true, newFailure(KindInvalidNoteProof, "read_knowledge_manifest", "manifest head is not a regular blob", false, "commit a regular manifest head file")
	}
	archive, err := runGit(ctx, repo, "archive", "--format=tar", commit, "--", knowledgeShardRoot)
	if err != nil {
		return knowledgeShards{}, true, wrapFailure(KindInvalidNoteProof, "read_knowledge_manifest", "cannot read the committed knowledge shards", true, "restore the shard blobs and retry", err)
	}
	shards := knowledgeShards{records: map[string][]byte{}}
	reader := tar.NewReader(bytes.NewReader(archive))
	for {
		header, err := reader.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return knowledgeShards{}, true, wrapFailure(KindInvalidNoteProof, "read_knowledge_manifest", "knowledge shard archive is malformed", false, "repair the canonical git tree", err)
		}
		if header.Typeflag != tar.TypeReg {
			continue
		}
		name := path.Clean(header.Name)
		if header.Size > maxKnowledgeManifest {
			return knowledgeShards{}, true, newFailure(KindInvalidNoteProof, "read_knowledge_manifest", "knowledge shard exceeds the bounded size: "+name, false, "publish a bounded shard")
		}
		content, err := io.ReadAll(io.LimitReader(reader, maxKnowledgeManifest+1))
		if err != nil {
			return knowledgeShards{}, true, wrapFailure(KindInvalidNoteProof, "read_knowledge_manifest", "cannot read a knowledge shard from the archive", false, "repair the canonical git tree", err)
		}
		if !shards.accept(name, content) {
			continue
		}
	}
	return shards, true, nil
}

// readKnowledgeShardsWorkingTree reads the shard tree from the checked-out
// repository, the form lesson publication edits before it commits.
func readKnowledgeShardsWorkingTree(repo string) (knowledgeShards, error) {
	head, err := os.ReadFile(path.Join(repo, knowledgeHeadPath)) //nolint:gosec // knowledgeHeadPath is fixed and repo is the operator-selected Git authority.
	if err != nil {
		return knowledgeShards{}, wrapFailure(KindGitUnreachable, "read_knowledge_manifest", "cannot read the knowledge manifest head", true, "restore the git knowledge home", err)
	}
	shards := knowledgeShards{head: head, records: map[string][]byte{}}
	if registry, err := os.ReadFile(path.Join(repo, knowledgeRegistryPath)); err == nil { //nolint:gosec // knowledgeRegistryPath is fixed and repo is the operator-selected Git authority.
		shards.registry = registry
	} else if !errors.Is(err, os.ErrNotExist) {
		return knowledgeShards{}, wrapFailure(KindGitUnreachable, "read_knowledge_manifest", "cannot read the domain registry shard", true, "restore the git knowledge home", err)
	}
	entries, err := os.ReadDir(path.Join(repo, knowledgeRecordTree))
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return knowledgeShards{}, wrapFailure(KindGitUnreachable, "read_knowledge_manifest", "cannot list the knowledge record shards", true, "restore the git knowledge home", err)
	}
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".json") {
			continue
		}
		content, err := os.ReadFile(path.Join(repo, knowledgeRecordTree, entry.Name())) //nolint:gosec // the record tree is fixed and repo is the operator-selected Git authority.
		if err != nil {
			return knowledgeShards{}, wrapFailure(KindGitUnreachable, "read_knowledge_manifest", "cannot read a knowledge record shard", true, "restore the git knowledge home", err)
		}
		shards.records[entry.Name()] = content
	}
	return shards, nil
}

// accept places one archive member into the shards by path. Members outside
// the three shard locations are ignored; coverage shards and other files
// under docs/knowledge are not part of the manifest.
func (s *knowledgeShards) accept(name string, content []byte) bool {
	switch {
	case name == knowledgeHeadPath:
		s.head = content
	case name == knowledgeRegistryPath:
		s.registry = content
	case strings.HasPrefix(name, knowledgeRecordTreeDir) && strings.HasSuffix(name, ".json") && !strings.Contains(strings.TrimPrefix(name, knowledgeRecordTreeDir), "/"):
		s.records[strings.TrimPrefix(name, knowledgeRecordTreeDir)] = content
	default:
		return false
	}
	return true
}
