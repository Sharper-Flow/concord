package store

import "testing"

// A version conflict reports that a subject holds a version other than the one
// the caller pinned, and every layer above reads its carrier to say which. A
// subject that does not exist holds no version, so classifying its absence as a
// version conflict produces a refusal that names nothing. The constructor
// decides between the two conditions so no call site has to.
func TestVersionConflictOnAnAbsentSubjectNamesTheAbsence(t *testing.T) {
	f := versionConflict(SubjectWorkItem, "work-absent", 1, 0, false)
	if f.Kind == KindVersionConflict {
		t.Fatalf("an absent subject must not be reported as a version conflict")
	}
	if f.Kind != KindProjectionNotFound {
		t.Fatalf("failure kind=%q, want %q", f.Kind, KindProjectionNotFound)
	}
	if len(f.CurrentVersions) != 0 {
		t.Fatalf("an absent subject carries no current version, got %+v", f.CurrentVersions)
	}
}

// The live-subject branch keeps the carrier the layers above depend on.
func TestVersionConflictOnALiveSubjectCarriesItsVersion(t *testing.T) {
	f := versionConflict(SubjectWorkItem, "work-1", 1, 3, true)
	if f.Kind != KindVersionConflict {
		t.Fatalf("failure kind=%q, want %q", f.Kind, KindVersionConflict)
	}
	if len(f.CurrentVersions) != 1 {
		t.Fatalf("current versions len=%d, want 1", len(f.CurrentVersions))
	}
	got := f.CurrentVersions[0]
	if got.SubjectType != SubjectWorkItem || got.SubjectID != "work-1" || got.Version != 3 {
		t.Fatalf("current version=%+v, want work_item/work-1/3", got)
	}
}
