// Package receipt renders the one operator-facing closure receipt a
// completed work item produces. The format is product-owned law (CD-0169):
// this package is its only owner, the `concord receipt` verb is its only
// render surface, and the adapter delegates to that verb instead of
// formatting anything itself.
//
// The receipt is an unfenced single-column markdown table: the plane line is
// the table's header row, the title row carries the summary mark, and one
// row follows per verified criterion, so alignment belongs to the host's
// markdown renderer and to nothing else. Column math over emoji-width
// glyphs is deliberately absent. Content law stays CD-0170: completed
// lifecycle only, verified contract predicates only, and an ambiguous
// projection degrades by omission.
package receipt

import (
	"context"
	"encoding/json"
	"strings"

	"github.com/sharper-flow/concord/internal/store"
)

// labelMaxColumns is the single-column cell width: a criterion label longer
// than this many code points truncates to the width minus one plus an
// ellipsis, so the cell stays bounded instead of wrapping the table.
const labelMaxColumns = 64

const (
	// markSummary is U+2705, the mark on the receipt's one summary row: the
	// work title, which states what the completion delivered.
	markSummary = "✅"
	// markCriterion is U+2713, the mark on every verified criterion row.
	// Every listed criterion is gate-verified; the mark states membership
	// in the verified set, not the predicate's kind. The bare form carries
	// no variation selector, so it stays a narrow glyph in every host.
	markCriterion = "✓"
	// planeHeader is U+1F6EB, which opens the table's header row.
	planeHeader = "🛫"
	// labelSeparator joins the typed fields inside one label. A middle dot
	// keeps every cell pipe-free, so no cell ever needs markdown escaping.
	labelSeparator = " · "
)

// Render formats the closure receipt for one pin. A pin outside the
// completed lifecycle renders nothing: the receipt is a completion receipt,
// and cancelled or superseded closures stay silent. Work ID in, string out.
func Render(pin store.WorkPin) string {
	if pin.Lifecycle != "completed" {
		return ""
	}
	header := planeHeader + " " + sanitize(pin.WorkID) + " Complete"
	if key := sanitize(pin.LinearIssueKey); key != "" {
		header = planeHeader + " " + key + " Complete (" + sanitize(pin.WorkID) + ")"
	}
	title := abbreviate(sanitize(pin.Title), labelMaxColumns)
	rows := criterionRows(pin.VerifiedCriteria)
	if title == "" && len(rows) == 0 {
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
	for _, label := range rows {
		b.WriteString("\n| ")
		b.WriteString(markCriterion)
		b.WriteString(" ")
		b.WriteString(label)
		b.WriteString(" |")
	}
	return b.String()
}

// RenderForWork reads the pin through the one store read path and renders
// the receipt for it. An unknown work item or an unreadable projection is a
// store failure; a completed projection with no renderable criteria renders
// the header alone.
func RenderForWork(ctx context.Context, s *store.Store, workID string) (string, error) {
	pin, err := store.ReadWorkPin(ctx, s, workID)
	if err != nil {
		return "", err
	}
	return Render(pin), nil
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
// fields CD-0170 D3 binds: the outcome kind with its first subject and
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
// would corrupt either the header line or a table cell.
func sanitize(value string) string {
	clean := make([]rune, 0, len(value))
	for _, r := range value {
		if r > 0x1f && r != 0x7f && r != '|' {
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
