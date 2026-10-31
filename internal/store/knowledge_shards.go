package store

import (
	"archive/tar"
	"bytes"
	"context"
	"errors"
	"io"
	"path"
	"strings"
)

// Historical committed-shard reading. The manifest document these layouts
// carry retired as authority with the law folder; the layout reader survives
// only where committed history still names it — the outside-law attribution
// reads candidate branches in the layout that revision actually carries, and
// the worktree audit reads lesson record paths. No runtime read answers from
// these shards.
const (
	knowledgeShardRoot     = ".concord/docs/knowledge"
	knowledgeHeadPath      = ".concord/docs/knowledge/manifest.json"
	knowledgeRegistryPath  = ".concord/docs/knowledge/domain-registry.json"
	knowledgeRecordTree    = ".concord/docs/knowledge/records"
	knowledgeRecordTreeDir = knowledgeRecordTree + "/"
	// manifestRecordPathPrefix and its pre-migration twin are the record path
	// tiers the historical shard layouts carry. They name committed history,
	// not runtime authority: the law folder is the acceptance authority.
	manifestRecordPathPrefix             = ".concord/docs/"
	preMigrationManifestRecordPathPrefix = "docs/"
	// maxKnowledgeRecord bounds one historical record shard the layout
	// reader reads from committed candidate-branch history.
	maxKnowledgeRecord = 16 * 1024
)

// knowledgeShardLayout is one shard-home tier a committed revision may
// carry (CD-0194 D5). The runtime law folder replaced this layout as
// authority; the layout reader survives only for committed history that
// still names it: the outside-law attribution reads candidate branches in
// the layout that revision actually carries.
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
	shards := knowledgeShards{records: map[string][]byte{}, recordPathPrefix: layout.recordPathPrefix}
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
		if name != layout.headPath && name != layout.registryPath && !directShardChild(name, layout.recordTreeDir) {
			continue
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
