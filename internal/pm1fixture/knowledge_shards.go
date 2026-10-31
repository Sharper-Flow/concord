package pm1fixture

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"sort"

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

// CommittedRegistryContentHash is the fingerprint the architecture binding
// records for one committed registry: the normalized registry's canonical
// JSON SHA-256, the same derivation the store applies to the snapshot's
// registry. Fixture bindings record it from committed registry metadata.
func CommittedRegistryContentHash(registry store.KnowledgeDomainRegistry) string {
	normalized := store.KnowledgeDomainRegistry{
		SchemaVersion: registry.SchemaVersion,
		ProductKey:    registry.ProductKey,
		RootDomainID:  registry.RootDomainID,
		Domains:       make([]store.KnowledgeDomain, len(registry.Domains)),
	}
	for index, domain := range registry.Domains {
		normalized.Domains[index] = domain
		normalized.Domains[index].ArchitectureRelations = make([]store.KnowledgeArchitectureRelation, len(domain.ArchitectureRelations))
		for relationIndex, relation := range domain.ArchitectureRelations {
			normalizedRelation := relation
			normalizedRelation.GoverningLawIDs = append([]string(nil), relation.GoverningLawIDs...)
			sort.Strings(normalizedRelation.GoverningLawIDs)
			normalized.Domains[index].ArchitectureRelations[relationIndex] = normalizedRelation
		}
		sort.Slice(normalized.Domains[index].ArchitectureRelations, func(left, right int) bool {
			a, b := normalized.Domains[index].ArchitectureRelations[left], normalized.Domains[index].ArchitectureRelations[right]
			if a.Kind != b.Kind {
				return a.Kind < b.Kind
			}
			if a.TargetDomainID != b.TargetDomainID {
				return a.TargetDomainID < b.TargetDomainID
			}
			return a.State < b.State
		})
	}
	sort.Slice(normalized.Domains, func(left, right int) bool {
		return normalized.Domains[left].DomainID < normalized.Domains[right].DomainID
	})
	canonical, err := json.Marshal(normalized)
	if err != nil {
		return ""
	}
	sum := sha256.Sum256(canonical)
	return "sha256:" + hex.EncodeToString(sum[:])
}
