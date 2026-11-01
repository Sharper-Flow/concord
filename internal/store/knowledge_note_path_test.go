package store

import "testing"

// Canonical note publication keeps admitting the law decisions tree a
// recorded proof can name (CD-0194 D5), alongside the current and
// pre-migration docs homes; other law subtrees are not note homes.
func TestValidateNotePathAdmitsLawDecisionNotes(t *testing.T) {
	t.Parallel()
	for _, admitted := range []string{
		".concord/law/decisions/CD-0300-synthetic.md",
		".concord/docs/decisions/CD-0300-synthetic.md",
		"docs/decisions/CD-0300-synthetic.md",
	} {
		if err := validateNotePath(admitted); err != nil {
			t.Errorf("validateNotePath(%q) = %v, want nil", admitted, err)
		}
	}
	for _, refused := range []string{
		".concord/law/work/note.md",
		".concord/law/constitution/founding.md",
	} {
		if err := validateNotePath(refused); err == nil {
			t.Errorf("validateNotePath(%q) = nil, want a placement refusal", refused)
		}
	}
}
