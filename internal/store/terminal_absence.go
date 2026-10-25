package store

import (
	"context"
	"fmt"
)

// Recurrence admission owns these classifications and their Product/workflow
// family scope even after execution collection (CD-0211 D2–D4).
const retainedDefectClassificationSQL = `(w.kind='bug' OR (w.kind='research' AND coalesce(json_extract(w.intent_json,'$.defect.failure_shape'),'')<>''))`

func retainedExecutionExistsQ(ctx context.Context, q queryer, workID string) (bool, error) {
	var retained bool
	err := q.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM work_items w WHERE w.id=?1 AND NOT (`+retainedDefectClassificationSQL+` AND w.lifecycle IN ('completed','cancelled','superseded') AND NOT EXISTS(SELECT 1 FROM effect_receipts r WHERE r.subject_type='work_item' AND r.subject_id=w.id) AND NOT EXISTS(SELECT 1 FROM durable_operations d WHERE d.work_id=w.id)))`, workID).Scan(&retained)
	return retained, err
}

// publishedWorkAbsenceQ distinguishes a published outcome from retained
// execution. Callers authorize scope before exposing its canonical locator.
// The index owns the Git-derived receipt; this read never probes Git or opens
// another connection while its caller holds a transaction.
func publishedWorkAbsenceQ(ctx context.Context, q queryer, workID, op string) error {
	return publishedWorkAbsenceForScopeQ(ctx, q, workID, "", "", op)
}

func publishedWorkAbsenceForScopeQ(ctx context.Context, q queryer, workID, productID, projectID, op string) error {
	live, err := retainedExecutionExistsQ(ctx, q, workID)
	if err != nil {
		return wrapFailure(KindUnavailable, op, "cannot read execution presence", true, "retry once the database is readable", err)
	}
	if live {
		return nil
	}
	rows, err := q.QueryContext(ctx, `SELECT a.home_project_id,a.home_locator_id,a.note_path,a.commit_oid,a.content_hash,
		(?2='' OR EXISTS(SELECT 1 FROM archived_work_products s WHERE s.home_project_id=a.home_project_id AND s.home_locator_id=a.home_locator_id AND s.work_id=a.id AND s.product_id=?2))
		AND (?3='' OR EXISTS(SELECT 1 FROM archived_work_projects p WHERE p.home_project_id=a.home_project_id AND p.home_locator_id=a.home_locator_id AND p.work_id=a.id AND p.project_id=?3)) AS in_scope
		FROM archived_work a WHERE a.id=?1 AND a.type='work_note' ORDER BY in_scope DESC,a.home_project_id,a.home_locator_id LIMIT 2`, workID, productID, projectID)
	if err != nil {
		return wrapFailure(KindUnavailable, op, "cannot read the published outcome locator", true, "retry once the database is readable", err)
	}
	defer rows.Close()
	var locator string
	var candidates []string
	count := 0
	for rows.Next() {
		var project, home, path, commit, hash string
		var inScope bool
		if err := rows.Scan(&project, &home, &path, &commit, &hash, &inScope); err != nil {
			return wrapFailure(KindUnavailable, op, "cannot decode the published outcome locator", true, "retry once the database is readable", err)
		}
		count++
		if inScope {
			candidates = append(candidates, home)
		}
		locator = fmt.Sprintf("home_project_id=%q home_locator_id=%q note_path=%q commit_oid=%q content_hash=%q", project, home, path, commit, hash)
	}
	if err := rows.Err(); err != nil {
		return wrapFailure(KindUnavailable, op, "cannot finish reading the published outcome locator", true, "retry once the database is readable", err)
	}
	if count == 0 {
		var linked bool
		if err := q.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM linear_issue_links WHERE work_id=?)`, workID).Scan(&linked); err != nil {
			return wrapFailure(KindUnavailable, op, "cannot read retained work identity", true, "retry once the database is readable", err)
		}
		if linked {
			return newFailure(KindProjectionNotFound, op, "work "+workID+" execution state is not retained; canonical outcome is not indexed", false, "resolve the canonical knowledge home for work "+workID+"; preserve its recorded issue identity")
		}
		classification, err := workExistsCore(ctx, q, workID)
		if err != nil {
			return err
		}
		if classification {
			return newFailure(KindUnavailable, op, "execution state is not retained and its canonical outcome is not indexed", false, "refresh the canonical knowledge home before resolving the outcome")
		}
		return nil
	}
	if count > 1 {
		failure := newFailure(KindAmbiguousScope, op, "published work has multiple canonical outcome identities", false, "resolve the canonical outcome identity before reading removed execution")
		failure.CandidateIDs = candidates
		return failure
	}
	absence := "execution state is not retained; canonical outcome: " + locator
	return newFailure(KindProjectionNotFound, op, "work "+workID+" "+absence, false, "read the published outcome instead; "+absence)
}

// Project-scoped readers must establish membership from the retained outcome
// before returning its locator. Live execution keeps its existing guards.
func publishedWorkAbsenceInScopeQ(ctx context.Context, q queryer, workID, productID, projectID, op string) error {
	live, err := retainedExecutionExistsQ(ctx, q, workID)
	if err != nil {
		return wrapFailure(KindUnavailable, op, "cannot read execution presence", true, "retry once the database is readable", err)
	}
	if live {
		return nil
	}
	var published, member bool
	if err := q.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM archived_work WHERE id=? AND type='work_note'), EXISTS(
		SELECT 1 FROM archived_work a WHERE a.id=? AND a.type='work_note'
		AND (?='' OR EXISTS(SELECT 1 FROM archived_work_projects p WHERE p.home_project_id=a.home_project_id AND p.home_locator_id=a.home_locator_id AND p.work_id=a.id AND p.project_id=?))
		AND (?='' OR (EXISTS(SELECT 1 FROM archived_work_products s WHERE s.home_project_id=a.home_project_id AND s.home_locator_id=a.home_locator_id AND s.work_id=a.id AND s.product_id=?)
		AND (?='' OR EXISTS(SELECT 1 FROM product_projects pp WHERE pp.project_id=? AND pp.product_id=?)))))`, workID, workID, projectID, projectID, productID, productID, projectID, projectID, productID).Scan(&published, &member); err != nil {
		return wrapFailure(KindUnavailable, op, "cannot read published work scope", true, "retry once the database is readable", err)
	}
	if published && !member {
		return newFailure(KindUnknownScope, op, "work reference is outside the requested Product/Project scope", false, "select a Product and Project the work belongs to")
	}
	return publishedWorkAbsenceForScopeQ(ctx, q, workID, productID, projectID, op)
}
