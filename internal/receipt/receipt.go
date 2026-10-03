// Package receipt renders the one operator-facing closure receipt a
// completed work item produces. The format is product-owned law (CD-0169,
// amended by CD-0202): this package is its only owner, the `concord receipt`
// verb is its only render surface, and the adapter delegates to that verb
// instead of formatting anything itself.
//
// The receipt is an unfenced single-column markdown table: the plane line is
// the table's header row, the title row carries the summary mark, and one
// row follows per verified criterion and per closure fact, so alignment
// belongs to the host's markdown renderer and to nothing else. Column math
// over emoji-width glyphs is deliberately absent. Content law stays CD-0170
// as amended by CD-0202: completed lifecycle only, verified contract
// predicates plus the recorded proposal, delivery, exclusion, and follow-up
// facts the store holds, and an ambiguous projection degrades by omission.
package receipt

import (
	"context"
	"encoding/json"
	"strings"

	"github.com/sharper-flow/concord/internal/store"
)

// labelMaxColumns is the criterion cell width: a criterion label longer than
// this many code points truncates to the width minus one plus an ellipsis,
// so the cell stays bounded instead of wrapping the table.
const labelMaxColumns = 64

// proseMaxColumns is the prose cell width CD-0202 binds: a problem, an
// excluded-scope entry, or a follow-up title longer than this many code
// points truncates to the width minus one plus an ellipsis. The cap keeps
// one long statement from dominating the banner without cutting most
// problem statements to a fragment the way the criterion width would.
const proseMaxColumns = 120

const (
	// markSummary is U+2705, the mark on the receipt's one summary row: the
	// work title, which states what the completion delivered.
	markSummary = "✅"
	// markCriterion is U+2713, the mark on every verified criterion row.
	// Every listed criterion is gate-verified; the mark states membership
	// in the verified set, not the predicate's kind. The bare form carries
	// no variation selector, so it stays a narrow glyph in every host.
	markCriterion = "✓"
	// markProblem is U+2753, the mark on the problem row: the problem the
	// recorded proposal stated and this completion answered.
	markProblem = "❓"
	// markShipped is U+1F4E6, the mark on the shipped row: the effective
	// delivery artifact, correction overlay applied.
	markShipped = "📦"
	// markLeftOut is U+2796, the mark on every left-out row: scope the
	// proposal excluded from this work.
	markLeftOut = "➖"
	// markFollowUp is U+1F517, the mark on every follow-up row: a work item
	// linked raised_from the closed item.
	markFollowUp = "🔗"
	// planeHeader is U+1F6EB, which opens the table's header row.
	planeHeader = "🛫"
	// labelSeparator joins the typed fields inside one label. A middle dot
	// keeps every cell pipe-free, so no cell ever needs markdown escaping.
	labelSeparator = " · "
)

// Render formats the closure receipt for one closure value. A pin outside
// the completed lifecycle renders nothing: the receipt is a completion
// receipt, and cancelled or superseded closures stay silent. An empty
// section renders no row, so a closure that carries none of the CD-0202
// facts renders the receipt bytes the pin alone produced before them.
func Render(closure store.WorkClosure) string {
	pin := closure.Pin
	if pin.Lifecycle != "completed" {
		return ""
	}
	header := planeHeader + " " + sanitize(pin.WorkID) + " Complete"
	if key := sanitize(pin.LinearIssueKey); key != "" {
		header = planeHeader + " " + key + " Complete (" + sanitize(pin.WorkID) + ")"
	}
	title := abbreviate(sanitize(pin.Title), labelMaxColumns)
	rows := criterionRows(pin.VerifiedCriteria)
	problem := problemRows(closure)
	shipped := shippedRows(closure)
	leftOut := leftOutRows(closure)
	followUps := followUpRows(closure)
	if title == "" && len(rows) == 0 && len(problem) == 0 && len(shipped) == 0 && len(leftOut) == 0 && len(followUps) == 0 {
		return header
	}
	var b strings.Builder
	b.WriteString("| ")
	b.WriteString(header)
	b.WriteString(" |\n| :-- |")
	if title != "" {
		b.WriteString("\n| ")
		b.WriteString(markSummary)
		b.WriteString(" ")
		b.WriteString(title)
		b.WriteString(" |")
	}
	for _, label := range problem {
		b.WriteString("\n| ")
		b.WriteString(markProblem)
		b.WriteString(" ")
		b.WriteString(label)
		b.WriteString(" |")
	}
	for _, label := range shipped {
		b.WriteString("\n| ")
		b.WriteString(markShipped)
		b.WriteString(" ")
		b.WriteString(label)
		b.WriteString(" |")
	}
	for _, label := range rows {
		b.WriteString("\n| ")
		b.WriteString(markCriterion)
		b.WriteString(" ")
		b.WriteString(label)
		b.WriteString(" |")
	}
	for _, label := range leftOut {
		b.WriteString("\n| ")
		b.WriteString(markLeftOut)
		b.WriteString(" ")
		b.WriteString(label)
		b.WriteString(" |")
	}
	for _, label := range followUps {
		b.WriteString("\n| ")
		b.WriteString(markFollowUp)
		b.WriteString(" ")
		b.WriteString(label)
		b.WriteString(" |")
	}
	return b.String()
}

// RenderForWork reads the closure through the one store read path and
// renders the receipt for it. An unknown work item or an unreadable
// projection is a store failure; a completed projection with no renderable
// fact renders the header alone.
func RenderForWork(ctx context.Context, s *store.Store, workID string) (string, error) {
	closure, err := store.ReadWorkClosure(ctx, s, workID)
	if err != nil {
		return "", err
	}
	return Render(closure), nil
}

// problemRows pairs the problem row with its abbreviated label. A blank or
// unsanitary problem renders no row: the receipt restates recorded facts,
// never an empty placeholder.
func problemRows(closure store.WorkClosure) []string {
	problem := strings.TrimSpace(abbreviate(sanitize(closure.Problem), proseMaxColumns))
	if problem == "" {
		return nil
	}
	return []string{problem}
}

// shippedRows pairs the shipped row with the effective delivery artifact.
// The artifact is a typed reference the store bounds, so it renders whole;
// a closure without an assertion renders no row.
func shippedRows(closure store.WorkClosure) []string {
	artifact := strings.TrimSpace(sanitize(closure.DeliveryArtifact))
	if artifact == "" {
		return nil
	}
	return []string{artifact}
}

// leftOutRows renders one row per excluded-scope entry, each abbreviated at
// the prose width. An entry that sanitizes to nothing renders no row.
func leftOutRows(closure store.WorkClosure) []string {
	rows := make([]string, 0, len(closure.OutOfScope))
	for _, entry := range closure.OutOfScope {
		label := strings.TrimSpace(abbreviate(sanitize(entry), proseMaxColumns))
		if label == "" {
			continue
		}
		rows = append(rows, label)
	}
	return rows
}

// followUpRows renders one row per raised follow-up: the confirmed Linear
// key when one exists, the work ID otherwise, then the item title. The
// identifier is a typed reference the store bounds; the title abbreviates
// at the prose width.
func followUpRows(closure store.WorkClosure) []string {
	rows := make([]string, 0, len(closure.FollowUps))
	for _, followUp := range closure.FollowUps {
		identifier := strings.TrimSpace(sanitize(followUp.LinearIssueKey))
		if identifier == "" {
			identifier = strings.TrimSpace(sanitize(followUp.WorkID))
		}
		if identifier == "" {
			continue
		}
		label := identifier
		if title := strings.TrimSpace(abbreviate(sanitize(followUp.Title), proseMaxColumns)); title != "" {
			label = identifier + labelSeparator + title
		}
		rows = append(rows, label)
	}
	return rows
}

// criterionRows pairs each verified criterion with its abbreviated typed
// label. A criterion whose latest verdict is not ok is not verified and
// drops, as does a criterion whose typed payload cannot form a label: the
// receipt restates proof, never an unverified claim.
func criterionRows(criteria []store.WorkPinVerifiedCriterion) []string {
	rows := make([]string, 0, len(criteria))
	for _, criterion := range criteria {
		if criterion.VerdictKind != "ok" {
			continue
		}
		label := criterionLabel(criterion)
		if label == "" {
			continue
		}
		rows = append(rows, abbreviate(label, labelMaxColumns))
	}
	return rows
}

// criterionPayload mirrors the typed outcome payloads the store projects.
type criterionPayload struct {
	Kind           string   `json:"kind"`
	Subjects       []string `json:"subjects"`
	Surface        string   `json:"surface"`
	Allowed        []string `json:"allowed"`
	CheckRef       string   `json:"check_ref"`
	ExpectedResult string   `json:"expected_result"`
}

// criterionLabel derives one typed label from the outcome payload, using the
// fields CD-0170 binds: the outcome kind with its first subject and
// surface, the first allowed token, or the check ref with its expected
// result. A payload outside those shapes renders no label.
func criterionLabel(criterion store.WorkPinVerifiedCriterion) string {
	var payload criterionPayload
	if !json.Valid(criterion.OutcomePayload) || json.Unmarshal(criterion.OutcomePayload, &payload) != nil {
		return ""
	}
	switch criterion.OutcomeKind {
	case "exists", "absent":
		if len(payload.Subjects) == 0 || payload.Subjects[0] == "" || payload.Surface == "" {
			return ""
		}
		return criterion.OutcomeKind + " " + sanitize(payload.Subjects[0]) + labelSeparator + sanitize(payload.Surface)
	case "outcome":
		if len(payload.Allowed) == 0 || payload.Allowed[0] == "" {
			return ""
		}
		return "outcome " + sanitize(payload.Allowed[0])
	case "check":
		if payload.CheckRef == "" || payload.ExpectedResult == "" {
			return ""
		}
		return "check " + sanitize(payload.CheckRef) + labelSeparator + sanitize(payload.ExpectedResult)
	default:
		return ""
	}
}

// sanitize drops control characters and pipes, the two byte classes that
// would corrupt either the header line or a table cell. A line break or tab
// becomes one space, so multi-line prose keeps its word boundaries.
func sanitize(value string) string {
	clean := make([]rune, 0, len(value))
	for _, r := range value {
		switch {
		case r == '\n' || r == '\r' || r == '\t':
			clean = append(clean, ' ')
		case r > 0x1f && r != 0x7f && r != '|':
			clean = append(clean, r)
		}
	}
	return string(clean)
}

// abbreviate cuts a label at the anchor-cell width. A longer label keeps its
// first width-1 code points and ends with an ellipsis, so the truncation is
// always visible rather than silent. A cut that lands on the field
// separator drops the dangling separator instead of ending the label with
// one.
func abbreviate(value string, width int) string {
	runes := []rune(value)
	if len(runes) <= width {
		return value
	}
	cut := strings.TrimRight(string(runes[:width-1]), " ·")
	return cut + "…"
}
