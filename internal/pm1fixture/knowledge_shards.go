package pm1fixture

import (
	"encoding/json"
	"fmt"

	"github.com/sharper-flow/concord/internal/store"
)

// The fixture repositories commit the Domain registry at its existing
// repository path, the one file of the retired knowledge shard tree retained
// consumers still read. The manifest head and the per-record shards were
// retired with the law index.
const knowledgeRegistryPath = ".concord/docs/knowledge/domain-registry.json"

// WriteKnowledgeShards lays the Domain registry out at its repository path,
// replacing any registry file already present. Test fixtures in other
// packages seed their designated homes through it.
func WriteKnowledgeShards(repo string, registry store.KnowledgeDomainRegistry) error {
	shard, err := json.MarshalIndent(registry, "", "  ")
	if err != nil {
		return fmt.Errorf("pm1fixture: encode domain registry shard: %w", err)
	}
	if err := writeKnowledgeFile(repo, knowledgeRegistryPath, string(shard)+"\n"); err != nil {
		return fmt.Errorf("pm1fixture: write domain registry shard: %w", err)
	}
	return nil
}
