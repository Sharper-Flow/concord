package store

import (
	"archive/tar"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
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
	// The package map and the coverage tree are read beside the manifest
	// at the same commit (CD-0222 D2). Only the knowledge index rebuild
	// parses them; a manifest read carries their bytes and ignores them.
	knowledgePackageMapPath  = ".concord/docs/knowledge/package-domains.json"
	knowledgeCoverageTreeDir = ".concord/docs/knowledge/coverage/"
	knowledgeFactTreeDir     = ".concord/docs/knowledge/facts/"
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
	// packageMapPath and coverageTreeDir are empty for a tier that predates
	// them, so such a revision carries neither.
	packageMapPath  string
	coverageTreeDir string
}

var knowledgeShardLayouts = []knowledgeShardLayout{
	{
		root:             knowledgeShardRoot,
		headPath:         knowledgeHeadPath,
		registryPath:     knowledgeRegistryPath,
		recordTree:       knowledgeRecordTree,
		recordTreeDir:    knowledgeRecordTreeDir,
		recordPathPrefix: manifestRecordPathPrefix,
		packageMapPath:   knowledgePackageMapPath,
		coverageTreeDir:  knowledgeCoverageTreeDir,
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
// validates record paths under the prefix that revision has. The package map
// and the coverage shards keyed by file name ride beside them for the
// knowledge index rebuild.
type knowledgeShards struct {
	head              []byte
	registry          []byte
	records           map[string][]byte
	recordPathPrefix  string
	packageMap        []byte
	packageMapPresent bool
	coverage          map[string][]byte
	facts             map[string][]byte
}

// composeKnowledgeManifest assembles the manifest document the shards
// describe and parses it under the same strict rules as an authored
// aggregate. Records are ordered by shard file name, which the generator
// pins to the record id.
func composeKnowledgeManifest(shards knowledgeShards, role knowledgeManifestRole) (KnowledgeManifest, error) {
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
	return parseKnowledgeManifestForRole(composed, pathPrefix, role)
}

// readKnowledgeShardsAtCommitRunner reads the shard tree at one commit
// through a single git archive call on the caller's runner, so a
// committed-read policy runner or an injected test runner executes the
// extraction. The second result is false when the commit carries no head
// shard in any layout, which is the shape a commit that predates the shards
// has. The newest layout the commit carries wins (CD-0194 D5).
func readKnowledgeShardsAtCommitRunner(ctx context.Context, runner GitRunner, repo, commit string) (knowledgeShards, bool, error) {
	for _, layout := range knowledgeShardLayouts {
		shards, sharded, err := readKnowledgeShardsAtCommitLayout(ctx, runner, repo, commit, layout)
		if err != nil || sharded {
			return shards, sharded, err
		}
	}
	return knowledgeShards{}, false, nil
}

func readKnowledgeShardsAtCommitLayout(ctx context.Context, runner GitRunner, repo, commit string, layout knowledgeShardLayout) (knowledgeShards, bool, error) {
	out, err := runner.Run(ctx, repo, "ls-tree", "-z", commit, "--", layout.headPath)
	if err != nil {
		return knowledgeShards{}, false, wrapFailure(KindGitUnreachable, "read_knowledge_manifest", "cannot inspect the knowledge shard tree", true, "restore the git object and retry", err)
	}
	entries, err := parseTreeEntries(out)
	if err != nil {
		return knowledgeShards{}, false, wrapFailure(KindInvalidNoteProof, "read_knowledge_manifest", "manifest tree entry is malformed", false, "repair the canonical git tree", err)
	}
	if len(entries) == 0 {
		if layout.root == knowledgeShardRoot {
			facts, err := runner.Run(ctx, repo, "ls-tree", "-z", commit, "--", strings.TrimSuffix(knowledgeFactTreeDir, "/"))
			if err != nil {
				return knowledgeShards{}, false, err
			}
			if len(facts) != 0 {
				return knowledgeShards{}, false, domainShapeProjectionFailure("fact shards require a committed knowledge manifest head")
			}
		}
		return knowledgeShards{}, false, nil
	}
	if entries[0].kind != "blob" || entries[0].mode != "100644" {
		return knowledgeShards{}, true, newFailure(KindInvalidNoteProof, "read_knowledge_manifest", "manifest head is not a regular blob", false, "commit a regular manifest head file")
	}
	archive, err := runner.Run(ctx, repo, "archive", "--format=tar", commit, "--", layout.root)
	if err != nil {
		return knowledgeShards{}, true, wrapFailure(KindInvalidNoteProof, "read_knowledge_manifest", "cannot read the committed knowledge shards", true, "restore the shard blobs and retry", err)
	}
	shards, err := readKnowledgeShardArchive(archive, layout)
	return shards, true, err
}

func readKnowledgeShardArchive(archive []byte, layout knowledgeShardLayout) (knowledgeShards, error) {
	shards := knowledgeShards{records: map[string][]byte{}, coverage: map[string][]byte{}, facts: map[string][]byte{}, recordPathPrefix: layout.recordPathPrefix}
	reader := tar.NewReader(bytes.NewReader(archive))
	for {
		header, err := reader.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return knowledgeShards{}, wrapFailure(KindInvalidNoteProof, "read_knowledge_manifest", "knowledge shard archive is malformed", false, "repair the canonical git tree", err)
		}
		name := path.Clean(header.Name)
		if name == strings.TrimSuffix(knowledgeFactTreeDir, "/") && header.Typeflag != tar.TypeDir {
			return knowledgeShards{}, domainShapeProjectionFailure("facts must be a committed directory")
		}
		isFact := layout.root == knowledgeShardRoot && strings.HasPrefix(name, knowledgeFactTreeDir)
		if isFact && header.Typeflag != tar.TypeDir && (header.Typeflag != tar.TypeReg || !directShardChild(name, knowledgeFactTreeDir) || header.Size > DomainFactMaxBytes) {
			return knowledgeShards{}, domainShapeProjectionFailure("fact shards must be bounded regular JSON files directly under " + knowledgeFactTreeDir)
		}
		if header.Typeflag != tar.TypeReg {
			continue
		}
		if header.Size > maxKnowledgeRecord {
			return knowledgeShards{}, newFailure(KindInvalidNoteProof, "read_knowledge_manifest", "knowledge shard exceeds the bounded size: "+name, false, "publish a bounded shard")
		}
		content, err := io.ReadAll(io.LimitReader(reader, maxKnowledgeRecord+1))
		if err != nil {
			return knowledgeShards{}, wrapFailure(KindInvalidNoteProof, "read_knowledge_manifest", "cannot read a knowledge shard from the archive", false, "repair the canonical git tree", err)
		}
		if !shards.accept(name, content) {
			continue
		}
		if len(shards.facts) > DomainShapeMaxItems {
			return knowledgeShards{}, domainShapeProjectionFailure("fact shard count exceeds 512")
		}
	}
	return shards, nil
}

// accept places one archive member into the shards by path. Members outside
// the shard locations of every layout tier are ignored. One archive call
// extracts exactly one tier, so the layouts cannot mix.
func (s *knowledgeShards) accept(name string, content []byte) bool {
	for _, layout := range knowledgeShardLayouts {
		switch {
		case name == layout.headPath:
			s.head = content
			s.recordPathPrefix = layout.recordPathPrefix
		case name == layout.registryPath:
			s.registry = content
		case directShardChild(name, layout.recordTreeDir):
			s.records[strings.TrimPrefix(name, layout.recordTreeDir)] = content
		case layout.packageMapPath != "" && name == layout.packageMapPath:
			s.packageMap, s.packageMapPresent = content, true
		case layout.coverageTreeDir != "" && directShardChild(name, layout.coverageTreeDir):
			s.coverage[strings.TrimPrefix(name, layout.coverageTreeDir)] = content
		case layout.root == knowledgeShardRoot && directShardChild(name, knowledgeFactTreeDir):
			if s.facts == nil {
				s.facts = map[string][]byte{}
			}
			s.facts[strings.TrimPrefix(name, knowledgeFactTreeDir)] = content
		default:
			continue
		}
		return true
	}
	return false
}

// directShardChild reports whether name is a JSON file directly under dir.
func directShardChild(name, dir string) bool {
	rest, found := strings.CutPrefix(name, dir)
	return found && strings.HasSuffix(rest, ".json") && !strings.Contains(rest, "/")
}
