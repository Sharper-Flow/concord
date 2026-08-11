// Package storeport adapts Concord's canonical Product-row read to the
// framework-independent launcher boundary.
package storeport

import (
	"context"
	"strconv"

	"github.com/sharper-flow/concord/internal/launcher"
	"github.com/sharper-flow/concord/internal/portfolio"
	"github.com/sharper-flow/concord/internal/store"
)

// Port accepts only the S1 portfolio read in this slice. Selection and
// filtering stay in memory and therefore cannot broaden the read surface.
type Port struct{ Store *store.Store }

func New(s *store.Store) *Port { return &Port{Store: s} }

func (p *Port) Read(ctx context.Context, request launcher.ReadRequest) (launcher.Snapshot, error) {
	if request.Kind != launcher.ReadPortfolio {
		return launcher.Snapshot{Screen: launcher.ScreenPortfolio, Coverage: "unavailable", StatusMessage: "unsupported_read"}, nil
	}
	result, err := portfolio.Read(ctx, p.Store, store.ProductRowRequest{Limit: request.Limit, Cursor: request.Cursor})
	if err != nil {
		return launcher.Snapshot{Screen: launcher.ScreenPortfolio, Coverage: "unreachable", StatusMessage: err.Error()}, err
	}
	return snapshotFromProductRows(result), nil
}

func FromProductRows(result store.ProductRowResult) launcher.Snapshot {
	result = portfolio.Map(result)
	rows := make([]launcher.ProductRow, 0, len(result.Rows))
	for _, row := range result.Rows {
		present := launcher.ProductRow{
			ID: row.ProductID, Name: row.DisplayName, NameSuffix: row.DisplayNameSuffix,
			Stage:         row.Stage.Maturity + "/" + row.Stage.AudienceCommitment,
			StageMaturity: row.Stage.Maturity, StageAudienceCommitment: row.Stage.AudienceCommitment,
			Reliance: row.Reliance.Authority, RelianceReason: row.Reliance.Reason,
			RelianceObservedAt: row.Reliance.ObservedAt, RelianceAge: row.Reliance.Age,
			RelianceStale: row.Reliance.Stale, BlocksExecution: row.Reliance.BlocksExecution,
			RelianceOmissions: cloneStrings(row.Reliance.Omissions), CountsState: row.ActionCounts.State,
			FocusAbsentReason: row.FocusAbsentReason,
		}
		if row.ActionCounts.Values != nil {
			present.InProgress = row.ActionCounts.Values.InProgress
			present.Blocked = row.ActionCounts.Values.Blocked
			present.Ready = row.ActionCounts.Values.Ready
			present.ActiveProblems = row.ActionCounts.Values.ActiveProblems
			present.ApprovalRequired = row.ActionCounts.Values.ApprovalRequired
			present.Actions = present.InProgress + present.Blocked + present.Ready + present.ActiveProblems + present.ApprovalRequired
		}
		if row.ActionCounts.Unavailable != nil {
			present.UnavailableReason = row.ActionCounts.Unavailable.Reason
			present.UnavailableOmissions = cloneStrings(row.ActionCounts.Unavailable.Omissions)
		}
		if row.Focus != nil {
			present.Focus = row.Focus.Title
			present.FocusID = row.Focus.WorkID
			present.FocusWorkKind = row.Focus.WorkKind
			present.FocusLifecycle = row.Focus.Lifecycle
			present.FocusAttentionKind = row.Focus.AttentionKind
			present.FocusPriority = row.Focus.Priority
			present.FocusWorkflowStepLabel = row.Focus.WorkflowStepLabel
			present.FocusProjectCount = row.Focus.ProjectCount
			present.FocusStageContext = row.Focus.StageContext.Kind
			if row.Focus.StageContext.FocusOverride != nil {
				present.FocusStageOverrideMaturity = row.Focus.StageContext.FocusOverride.Maturity
				present.FocusStageOverrideAudience = row.Focus.StageContext.FocusOverride.AudienceCommitment
			}
		}
		rows = append(rows, present)
	}
	return launcher.Snapshot{
		Screen: launcher.ScreenPortfolio, QueryID: result.QueryID, ContractVersion: result.ContractVersion,
		Watermark: strconv.FormatInt(result.SourceVersionWatermark, 10), SourceVersionWatermark: result.SourceVersionWatermark,
		ObservedAt: result.ObservedAt, Reliance: result.Authority, Coverage: result.Authority,
		OrderingKeys: append([]string(nil), result.OrderingKeys...), NextCursor: result.NextCursor,
		Rows: rows,
	}
}

func snapshotFromProductRows(result store.ProductRowResult) launcher.Snapshot {
	return FromProductRows(result)
}

func cloneStrings(values []string) []string {
	if values == nil {
		return nil
	}
	return append([]string{}, values...)
}
