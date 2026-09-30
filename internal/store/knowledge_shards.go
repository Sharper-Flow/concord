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

// The knowledge manifest is composed from shards under .concord/docs/knowledge/
// (CD-0114). The head shard carries the root fields, the domain registry is
// its own shard, and every record is one file under the record tree. No
// committed file lists every record, so two commits that each add a record
// never touch the same line.
const (
	knowledgeShardRoot     = ".concord/docs/knowledge"
	knowledgeHeadPath      = ".concord/docs/knowledge/manifest.json"
	knowledgeRegistryPath  = ".concord/docs/knowledge/domain-registry.json"
	knowledgeRecordTree    = ".concord/docs/knowledge/records"
	knowledgeRecordTreeDir = knowledgeRecordTree + "/"
)

// knowledgeShardLayout is one shard-home tier a revision may carry
// (CD-0194 D5). The first layout a commit carries wins; record paths in that
// revision validate under the same tier's prefix.
type knowledgeShardLayout struct {
	root             string
	headPath         string
	registryPath     string
	recordTree       string
	recordTreeDir    string
	recordPathPrefix string
}

var knowledgeShardLayouts = []knowledgeShardLayout{
	{
		root:             knowledgeShardRoot,
		headPath:         knowledgeHeadPath,
		registryPath:     knowledgeRegistryPath,
		recordTree:       knowledgeRecordTree,
		recordTreeDir:    knowledgeRecordTreeDir,
		recordPathPrefix: manifestRecordPathPrefix,
	},
	{
		root:             "docs/knowledge",
		headPath:         "docs/knowledge/manifest.json",
		registryPath:     "docs/knowledge/domain-registry.json",
		recordTree:       "docs/knowledge/records",
		recordTreeDir:    "docs/knowledge/records/",
		recordPathPrefix: preMigrationManifestRecordPathPrefix,
	},
}

// knowledgeShards is the raw material of one manifest: the head, the domain
// registry, and every record shard keyed by its file name. recordPathPrefix
// carries the layout tier the shards were read from, so composition
// validates record paths under the prefix that revision has.
type knowledgeShards struct {
	head             []byte
	registry         []byte
	records          map[string][]byte
	recordPathPrefix string
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
	pathPrefix := shards.recordPathPrefix
	if pathPrefix == "" {
		pathPrefix = manifestRecordPathPrefix
	}
	return parseKnowledgeManifestWithPaths(composed, pathPrefix)
}

// readKnowledgeShardsAtCommit reads the shard tree at one commit through a
// single git archive call. The second result is false when the commit carries
// no head shard in any layout, which is the shape a commit that predates the
// shards has. The newest layout the commit carries wins (CD-0194 D5).
func readKnowledgeShardsAtCommit(ctx context.Context, repo, commit string) (knowledgeShards, bool, error) {
	for _, layout := range knowledgeShardLayouts {
		shards, sharded, err := readKnowledgeShardsAtCommitLayout(ctx, repo, commit, layout)
		if err != nil || sharded {
			return shards, sharded, err
		}
	}
	return knowledgeShards{}, false, nil
}

func readKnowledgeShardsAtCommitLayout(ctx context.Context, repo, commit string, layout knowledgeShardLayout) (knowledgeShards, bool, error) {
	out, err := runGit(ctx, repo, "ls-tree", "-z", commit, "--", layout.headPath)
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
	archive, err := runGit(ctx, repo, "archive", "--format=tar", commit, "--", layout.root)
	if err != nil {
		return knowledgeShards{}, true, wrapFailure(KindInvalidNoteProof, "read_knowledge_manifest", "cannot read the committed knowledge shards", true, "restore the shard blobs and retry", err)
	}
	shards := knowledgeShards{records: map[string][]byte{}, recordPathPrefix: layout.recordPathPrefix}
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
		if header.Size > maxKnowledgeRecord {
			return knowledgeShards{}, true, newFailure(KindInvalidNoteProof, "read_knowledge_manifest", "knowledge shard exceeds the bounded size: "+name, false, "publish a bounded shard")
		}
		content, err := io.ReadAll(io.LimitReader(reader, maxKnowledgeRecord+1))
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
	shards := knowledgeShards{head: head, records: map[string][]byte{}, recordPathPrefix: manifestRecordPathPrefix}
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
// the three shard locations of every layout tier are ignored; coverage shards
// and other files under a shard home are not part of the manifest. One
// archive call extracts exactly one tier, so the layouts cannot mix.
func (s *knowledgeShards) accept(name string, content []byte) bool {
	for _, layout := range knowledgeShardLayouts {
		switch {
		case name == layout.headPath:
			s.head = content
			s.recordPathPrefix = layout.recordPathPrefix
		case name == layout.registryPath:
			s.registry = content
		case strings.HasPrefix(name, layout.recordTreeDir) && strings.HasSuffix(name, ".json") && !strings.Contains(strings.TrimPrefix(name, layout.recordTreeDir), "/"):
			s.records[strings.TrimPrefix(name, layout.recordTreeDir)] = content
		default:
			continue
		}
		return true
	}
	return false
}
