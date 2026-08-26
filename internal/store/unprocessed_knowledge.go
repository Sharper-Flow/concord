package store

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

const defaultKnowledgeRoot = "docs/"

// UnprocessedKnowledgeDocs returns sorted markdown paths that the manifest does
// not record, exclude, or dispose of.
func UnprocessedKnowledgeDocs(manifest KnowledgeManifest, repoRoot string) ([]string, error) {
	if repoRoot == "" {
		return nil, errors.New("knowledge repository root is empty")
	}
	roots := manifest.KnowledgeRoots
	if len(roots) == 0 {
		roots = []string{defaultKnowledgeRoot}
	}

	recorded := make(map[string]struct{}, len(manifest.Records))
	for _, record := range manifest.Records {
		recorded[normalizeKnowledgePath(record.Path)] = struct{}{}
	}
	disposed := make(map[string]struct{}, len(manifest.Dispositions))
	for _, disposition := range manifest.Dispositions {
		disposed[normalizeKnowledgePath(disposition.Path)] = struct{}{}
	}
	excludedFiles := make(map[string]struct{})
	var excludedPrefixes []string
	for _, exclusion := range manifest.Exclusions {
		if strings.HasSuffix(exclusion, "/") {
			excludedPrefixes = append(excludedPrefixes, exclusion)
		} else {
			excludedFiles[exclusion] = struct{}{}
		}
	}

	walked := make(map[string]struct{})
	for _, root := range roots {
		if err := walkKnowledgeRoot(repoRoot, root, walked); err != nil {
			return nil, err
		}
	}
	paths := make([]string, 0, len(walked))
	for candidate := range walked {
		if _, ok := recorded[candidate]; ok {
			continue
		}
		if _, ok := disposed[candidate]; ok {
			continue
		}
		if _, ok := excludedFiles[candidate]; ok || hasKnowledgePrefix(candidate, excludedPrefixes) {
			continue
		}
		paths = append(paths, candidate)
	}
	sort.Strings(paths)
	return paths, nil
}

func walkKnowledgeRoot(repoRoot, root string, found map[string]struct{}) error {
	relativeRoot := filepath.Clean(filepath.FromSlash(root))
	if root == "" || filepath.IsAbs(relativeRoot) || relativeRoot == ".." || strings.HasPrefix(relativeRoot, ".."+string(filepath.Separator)) {
		return newFailure(KindInvalidNoteProof, "unprocessed_knowledge", "knowledge root escapes the repository: "+root, false, "declare a non-empty repository-relative knowledge root")
	}
	absolute := filepath.Join(repoRoot, relativeRoot)
	err := filepath.WalkDir(absolute, func(candidate string, entry fs.DirEntry, walkErr error) error { //nolint:gosec // relativeRoot rejects absolute and parent traversal before joining it to repoRoot.
		if walkErr != nil {
			if errors.Is(walkErr, os.ErrNotExist) && candidate == absolute {
				return fs.SkipDir
			}
			return walkErr
		}
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".md") {
			return nil
		}
		relative, err := filepath.Rel(repoRoot, candidate)
		if err != nil {
			return fmt.Errorf("relativize knowledge path %q: %w", candidate, err)
		}
		found[normalizeKnowledgePath(filepath.ToSlash(relative))] = struct{}{}
		return nil
	})
	if errors.Is(err, fs.SkipDir) {
		return nil
	}
	return err
}

func hasKnowledgePrefix(candidate string, prefixes []string) bool {
	for _, prefix := range prefixes {
		if strings.HasPrefix(candidate, prefix) {
			return true
		}
	}
	return false
}

func normalizeKnowledgePath(value string) string {
	return strings.TrimLeft(strings.ReplaceAll(value, "\\", "/"), "/")
}

// ReadUnprocessedKnowledgeDocs reads the current committed manifest at the
// resolved knowledge home, then enumerates the checkout rooted at that home.
func (s *Store) ReadUnprocessedKnowledgeDocs(ctx context.Context, home KnowledgeHome) ([]string, error) {
	if s == nil || s.db == nil {
		return nil, newFailure(KindUnavailable, "PM1.Q15", "store is not open", false, "open a store before reading unprocessed knowledge")
	}
	commit, err := resolveKnowledgeHead(ctx, home)
	if err != nil {
		return nil, err
	}
	manifest, missing, err := readKnowledgeManifest(ctx, home.RepoPath, commit)
	if err != nil {
		return nil, err
	}
	if missing {
		return nil, newFailure(KindKnowledgeMissing, "PM1.Q15", "knowledge manifest is missing", false, "restore the manifest at the repository head")
	}
	return UnprocessedKnowledgeDocs(manifest, home.RepoPath)
}
