package store

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
)

// Migration 174 drops the legacy SQL law index. The committed law folder of
// each designated Product knowledge home is the only law authority, so the
// drop must not strand a law the index still names: before the migrating
// transaction opens, every legacy law_subjects row of a designated home must
// resolve, by stable ID, to a law the home's committed folder loads. The Git
// reads run outside the transaction (CD-0195 D2); the transaction re-reads the
// inventory and each home's head cheaply and applies the drop only while both
// still match what was accounted.
//
// Rows of a locator no Product designates are not Product law: resolution
// reads designated homes only (CD-0200), and the verified upgrade snapshot
// keeps them with every other retired row.
const lawIndexRetirementName = "law_index_retirement"

// lawIndexHome is one home's legacy law inventory with the Product binding and
// canonical path the accounting resolved it through.
type lawIndexHome struct {
	ProjectID  string          `json:"project_id"`
	LocatorID  string          `json:"locator_id"`
	RepoPath   string          `json:"repo_path"`
	ProductIDs []string        `json:"product_ids"`
	Laws       []lawIndexEntry `json:"laws"`
}

type lawIndexEntry struct {
	LawID string `json:"law_id"`
	Path  string `json:"path"`
}

func (home lawIndexHome) designated() bool { return len(home.ProductIDs) > 0 }

func (home lawIndexHome) knowledgeHome() KnowledgeHome {
	return KnowledgeHome{HomeProjectID: home.ProjectID, HomeLocatorID: home.LocatorID, RepoPath: home.RepoPath, HeadRef: "HEAD"}
}

// lawIndexRetirementProof is what the pre-transaction accounting established:
// the inventory it read and the head commit each designated home's folder
// accounted for it at.
type lawIndexRetirementProof struct {
	inventoryDigest string
	heads           map[string]string
}

func lawIndexHomeKey(projectID, locatorID string) string {
	return projectID + "\x00" + locatorID
}

// lawIndexRetirementPending reports whether migration 174 still has to run
// against a database whose legacy law index exists.
func lawIndexRetirementPending(ctx context.Context, db *sql.DB) (bool, error) {
	var present bool
	if err := db.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM sqlite_schema WHERE type='table' AND name='law_subjects')`).Scan(&present); err != nil {
		return false, wrapFailure(KindUnavailable, "migrate", "cannot inspect the legacy law index", true, "confirm the database is readable and retry upgrade", err)
	}
	return present, nil
}

// establishLawIndexRetirementProof accounts for the legacy law inventory
// against each designated home's committed law folder. It runs Git and must
// never run inside a transaction. A nil proof means there is no legacy index
// to account for.
func establishLawIndexRetirementProof(ctx context.Context, db *sql.DB) (*lawIndexRetirementProof, error) {
	pending, err := lawIndexRetirementPending(ctx, db)
	if err != nil || !pending {
		return nil, err
	}
	homes, digest, err := readLawIndexInventory(ctx, db)
	if err != nil {
		return nil, err
	}
	proof := &lawIndexRetirementProof{inventoryDigest: digest, heads: map[string]string{}}
	var refusals []string
	for _, home := range homes {
		if !home.designated() {
			continue
		}
		head, refusal := accountLawIndexHome(ctx, home)
		if refusal != "" {
			refusals = append(refusals, refusal)
			continue
		}
		proof.heads[lawIndexHomeKey(home.ProjectID, home.LocatorID)] = head
	}
	if len(refusals) > 0 {
		return nil, newFailure(KindUpgradeBlocked, "migrate",
			"migration 174 (law_index_retirement) would drop legacy law index rows the committed law folder cannot account for; no migration was applied: "+strings.Join(refusals, "; "),
			true, "commit each named law into its home's .concord/law/ folder with its stable ID in strict JSON frontmatter, repair each named folder file, or restore each named home's committed Git repository, then retry concord upgrade")
	}
	return proof, nil
}

// accountLawIndexHome loads one designated home's committed law folder and
// returns its head, or the refusal clause naming the Product, home, and laws
// the folder cannot account for.
func accountLawIndexHome(ctx context.Context, home lawIndexHome) (string, string) {
	subject := fmt.Sprintf("Product %s knowledge home %s/%s", strings.Join(home.ProductIDs, ", "), home.ProjectID, home.LocatorID)
	laws := lawIndexEntryList(home.Laws)
	if home.RepoPath == "" {
		return "", subject + " has no canonical path, so legacy law " + laws + " cannot be accounted for"
	}
	subject += " at " + home.RepoPath
	snapshot, err := loadLawFolderSnapshotAtHome(ctx, home.knowledgeHome())
	if err != nil {
		var failure *Failure
		if errors.As(err, &failure) && failure.Kind == KindGitUnreachable {
			return "", subject + " is unreachable (" + failure.Detail + "), so legacy law " + laws + " cannot be accounted for"
		}
		detail := err.Error()
		if errors.As(err, &failure) {
			detail = failure.Detail
		}
		return "", subject + " holds a law folder that does not load (" + detail + "), so legacy law " + laws + " cannot be accounted for"
	}
	var missing []lawIndexEntry
	for _, law := range home.Laws {
		if _, ok := snapshot.Law(law.LawID); !ok {
			missing = append(missing, law)
		}
	}
	if len(missing) > 0 {
		return "", subject + " commit " + snapshot.CommitOID() + " has no law folder file for legacy law " + lawIndexEntryList(missing)
	}
	return snapshot.CommitOID(), ""
}

func lawIndexEntryList(laws []lawIndexEntry) string {
	rendered := make([]string, 0, len(laws))
	for _, law := range laws {
		rendered = append(rendered, law.LawID+" (recorded at "+law.Path+")")
	}
	return strings.Join(rendered, ", ")
}

// readLawIndexInventory reads the legacy law inventory by home, with each
// home's designating Products and canonical path, and its digest. The digest
// binds everything the accounting depended on, so the transaction can confirm
// it accounted for the inventory it is about to drop. Only accepted rows are
// current law: a store older than schema 140 still holds superseded rows,
// which that migration drops inside the same upgrade transaction before the
// revalidation re-reads the inventory.
func readLawIndexInventory(ctx context.Context, q queryer) ([]lawIndexHome, string, error) {
	rows, err := q.QueryContext(ctx, `SELECT home_project_id,home_locator_id,law_id,path FROM law_subjects WHERE status='accepted' ORDER BY home_project_id,home_locator_id,law_id`)
	if err != nil {
		return nil, "", lawIndexInventoryFailure(err)
	}
	var homes []lawIndexHome
	for rows.Next() {
		var projectID, locatorID string
		var entry lawIndexEntry
		if err := rows.Scan(&projectID, &locatorID, &entry.LawID, &entry.Path); err != nil {
			_ = rows.Close()
			return nil, "", lawIndexInventoryFailure(err)
		}
		if len(homes) == 0 || homes[len(homes)-1].ProjectID != projectID || homes[len(homes)-1].LocatorID != locatorID {
			homes = append(homes, lawIndexHome{ProjectID: projectID, LocatorID: locatorID})
		}
		homes[len(homes)-1].Laws = append(homes[len(homes)-1].Laws, entry)
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return nil, "", lawIndexInventoryFailure(err)
	}
	_ = rows.Close()
	for index := range homes {
		home := &homes[index]
		err := q.QueryRowContext(ctx, `SELECT locator_value FROM project_locators WHERE project_id=? AND locator_id=? AND kind='canonical_path'`, home.ProjectID, home.LocatorID).Scan(&home.RepoPath)
		if err != nil && err != sql.ErrNoRows {
			return nil, "", lawIndexInventoryFailure(err)
		}
		products, err := q.QueryContext(ctx, `SELECT product_id FROM product_knowledge_homes WHERE project_id=? AND locator_id=? ORDER BY product_id`, home.ProjectID, home.LocatorID)
		if err != nil {
			return nil, "", lawIndexInventoryFailure(err)
		}
		for products.Next() {
			var productID string
			if err := products.Scan(&productID); err != nil {
				_ = products.Close()
				return nil, "", lawIndexInventoryFailure(err)
			}
			home.ProductIDs = append(home.ProductIDs, productID)
		}
		if err := products.Err(); err != nil {
			_ = products.Close()
			return nil, "", lawIndexInventoryFailure(err)
		}
		_ = products.Close()
	}
	encoded, err := json.Marshal(homes)
	if err != nil {
		return nil, "", lawIndexInventoryFailure(err)
	}
	sum := sha256.Sum256(encoded)
	return homes, hex.EncodeToString(sum[:]), nil
}

func lawIndexInventoryFailure(err error) error {
	return wrapFailure(KindUnavailable, "migrate", "cannot read the legacy law index inventory", true, "confirm the database is readable and retry upgrade", err)
}

// verifyLawIndexRetirementProof is the in-transaction revalidation: the
// inventory must be the one accounted for, and each accounted home's head
// must not have moved. It reads a few small files and runs no Git.
func verifyLawIndexRetirementProof(ctx context.Context, tx *sql.Tx, proof *lawIndexRetirementProof) error {
	homes, digest, err := readLawIndexInventory(ctx, tx)
	if err != nil {
		return err
	}
	designated := 0
	for _, home := range homes {
		if home.designated() {
			designated++
		}
	}
	if designated == 0 {
		return nil
	}
	if proof == nil || proof.inventoryDigest != digest {
		return lawIndexRetirementMoved("the legacy law index or its home bindings changed after the law folder accounted for them")
	}
	for _, home := range homes {
		if !home.designated() {
			continue
		}
		accounted := proof.heads[lawIndexHomeKey(home.ProjectID, home.LocatorID)]
		head, err := resolveKnowledgeHeadCheap(home.RepoPath, "HEAD")
		if err != nil || head != accounted {
			return lawIndexRetirementMoved(fmt.Sprintf("knowledge home %s/%s at %s moved from the commit its law folder accounted for", home.ProjectID, home.LocatorID, home.RepoPath))
		}
	}
	return nil
}

func lawIndexRetirementMoved(detail string) error {
	return newFailure(KindUpgradeBlocked, "migrate",
		"migration 174 (law_index_retirement) was not applied: "+detail, true,
		"retry concord upgrade; it accounts for the legacy law index again")
}
