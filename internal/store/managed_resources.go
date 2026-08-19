package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

const (
	maxManagedResourceMetadataBytes = 16 * 1024
	maxManagedResourceKindDetail    = 256
)

const (
	managedResourceEventCreated       = "managed_resource.created"
	managedResourceEventConsumerAdded = "managed_resource.consumer_added"
)

// ManagedResource is the canonical C15 identity and declarative inventory
// state. Native provider state is intentionally not part of this projection.
type ManagedResource struct {
	ResourceID              string          `json:"resource_id"`
	DisplayName             string          `json:"display_name"`
	Class                   string          `json:"class"`
	Kind                    string          `json:"kind"`
	Purpose                 string          `json:"purpose"`
	StageMaturity           string          `json:"stage_maturity"`
	StageAudienceCommitment string          `json:"stage_audience_commitment"`
	Environments            []string        `json:"environments"`
	LocatorAbsenceReason    string          `json:"locator_absence_reason,omitempty"`
	MetadataSchemaVersion   string          `json:"metadata_schema_version"`
	Metadata                json.RawMessage `json:"metadata"`
	Version                 int64           `json:"version"`
	CreatedAt               string          `json:"created_at"`
	UpdatedAt               string          `json:"updated_at"`
}

// ManagedResourceCreateRequest creates a canonical resource and its sole
// initial Product owner in one event-backed transaction.
type ManagedResourceCreateRequest struct {
	EventID                 string
	ResourceID              string
	ProductID               string
	DisplayName             string
	Class                   string
	Kind                    string
	Purpose                 string
	StageMaturity           string
	StageAudienceCommitment string
	Environments            []string
	LocatorAbsenceReason    string
	MetadataSchemaVersion   string
	Metadata                json.RawMessage
	OwnerPurpose            string
	OwnerEnvironments       []string
	ExpectedProductVersion  int64
	Actor                   string
	OccurredAt              time.Time
}

// AddManagedResourceConsumerRequest adds one Product consumer after an
// expected resource-version check.
type AddManagedResourceConsumerRequest struct {
	EventID                 string
	ResourceID              string
	ProductID               string
	Purpose                 string
	Environments            []string
	ExpectedResourceVersion int64
	Actor                   string
	OccurredAt              time.Time
}

type managedResourceCreatedPayload struct {
	ResourceID              string          `json:"resource_id"`
	ProductID               string          `json:"product_id"`
	DisplayName             string          `json:"display_name"`
	Class                   string          `json:"class"`
	Kind                    string          `json:"kind"`
	Purpose                 string          `json:"purpose"`
	StageMaturity           string          `json:"stage_maturity"`
	StageAudienceCommitment string          `json:"stage_audience_commitment"`
	Environments            []string        `json:"environments"`
	LocatorAbsenceReason    string          `json:"locator_absence_reason"`
	MetadataSchemaVersion   string          `json:"metadata_schema_version"`
	Metadata                json.RawMessage `json:"metadata"`
	OwnerPurpose            string          `json:"owner_purpose"`
	OwnerEnvironments       []string        `json:"owner_environments"`
}

type managedResourceConsumerAddedPayload struct {
	ResourceID       string   `json:"resource_id"`
	ProductID        string   `json:"product_id"`
	Purpose          string   `json:"purpose"`
	Environments     []string `json:"environments"`
	ExpectedVersion  int64    `json:"expected_version"`
	ResultingVersion int64    `json:"resulting_version"`
}

func CreateManagedResource(ctx context.Context, s *Store, req ManagedResourceCreateRequest) (ManagedResource, error) {
	resource := ManagedResource{
		ResourceID: req.ResourceID, DisplayName: req.DisplayName, Class: req.Class,
		Kind: req.Kind, Purpose: req.Purpose, StageMaturity: req.StageMaturity,
		StageAudienceCommitment: req.StageAudienceCommitment, Environments: req.Environments,
		LocatorAbsenceReason: req.LocatorAbsenceReason, MetadataSchemaVersion: req.MetadataSchemaVersion,
		Metadata: req.Metadata, Version: 1,
		CreatedAt: req.OccurredAt.UTC().Format(time.RFC3339Nano), UpdatedAt: req.OccurredAt.UTC().Format(time.RFC3339Nano),
	}
	if resource.Environments == nil {
		resource.Environments = []string{}
	}
	if err := validateManagedResourceInput(resource); err != nil {
		return ManagedResource{}, err
	}
	ownerPurpose := req.OwnerPurpose
	if ownerPurpose == "" {
		ownerPurpose = req.Purpose
	}
	ownerEnvironments := req.OwnerEnvironments
	if ownerEnvironments == nil {
		ownerEnvironments = append([]string(nil), req.Environments...)
	}
	if ownerEnvironments == nil {
		ownerEnvironments = []string{}
	}
	if err := validateResourceEnvironments(ownerEnvironments, resource.Environments, nil); err != nil {
		return ManagedResource{}, err
	}
	if ownerPurpose == "" || len(ownerPurpose) > 512 {
		return ManagedResource{}, newFailure(KindInvalidOperation, "create_managed_resource", "owner purpose must be bounded and non-empty", false, "supply a purpose of at most 512 characters")
	}
	if req.EventID == "" || req.ProductID == "" || req.Actor == "" || req.OccurredAt.IsZero() {
		return ManagedResource{}, newFailure(KindInvalidOperation, "create_managed_resource", "resource creation is missing event, Product, actor, or time", false, "supply all bounded operation identity fields")
	}
	payload, err := json.Marshal(managedResourceCreatedPayload{
		ResourceID: resource.ResourceID, ProductID: req.ProductID, DisplayName: resource.DisplayName,
		Class: resource.Class, Kind: resource.Kind, Purpose: resource.Purpose,
		StageMaturity: resource.StageMaturity, StageAudienceCommitment: resource.StageAudienceCommitment,
		Environments: resource.Environments, LocatorAbsenceReason: resource.LocatorAbsenceReason,
		MetadataSchemaVersion: resource.MetadataSchemaVersion, Metadata: resource.Metadata,
		OwnerPurpose: ownerPurpose, OwnerEnvironments: ownerEnvironments,
	})
	if err != nil {
		return ManagedResource{}, wrapFailure(KindInvalidPayload, "create_managed_resource", "cannot encode resource payload", false, "supply valid metadata", err)
	}
	operation := Operation{Events: []Event{{EventID: req.EventID, Kind: managedResourceEventCreated, SubjectType: SubjectProduct, SubjectID: req.ProductID, Actor: req.Actor, OccurredAt: req.OccurredAt, PayloadVersion: 1, Payload: payload}}}
	if req.ExpectedProductVersion > 0 {
		operation.ExpectedVersions = map[SubjectRef]int64{VersionRef(SubjectProduct, req.ProductID): req.ExpectedProductVersion}
	}
	if err := ApplyOperation(ctx, s, operation); err != nil {
		return ManagedResource{}, err
	}
	return resource, nil
}

func AddManagedResourceConsumer(ctx context.Context, s *Store, req AddManagedResourceConsumerRequest) error {
	if req.EventID == "" || req.ResourceID == "" || req.ProductID == "" || req.Actor == "" || req.OccurredAt.IsZero() || req.ExpectedResourceVersion < 1 {
		return newFailure(KindInvalidOperation, "add_managed_resource_consumer", "consumer operation is missing bounded identity or version fields", false, "supply resource, Product, event, actor, time, and a positive expected version")
	}
	if len(req.Purpose) == 0 || len(req.Purpose) > 512 {
		return newFailure(KindInvalidOperation, "add_managed_resource_consumer", "consumer purpose must be bounded and non-empty", false, "supply a purpose of at most 512 characters")
	}
	environments := req.Environments
	if environments == nil {
		environments = []string{}
	}
	if err := validateResourceEnvironments(environments, nil, nil); err != nil {
		return err
	}
	payload, err := json.Marshal(managedResourceConsumerAddedPayload{ResourceID: req.ResourceID, ProductID: req.ProductID, Purpose: req.Purpose, Environments: environments, ExpectedVersion: req.ExpectedResourceVersion, ResultingVersion: req.ExpectedResourceVersion + 1})
	if err != nil {
		return wrapFailure(KindInvalidPayload, "add_managed_resource_consumer", "cannot encode consumer payload", false, "supply valid consumer metadata", err)
	}
	return ApplyOperation(ctx, s, Operation{Events: []Event{{EventID: req.EventID, Kind: managedResourceEventConsumerAdded, SubjectType: SubjectProduct, SubjectID: req.ProductID, Actor: req.Actor, OccurredAt: req.OccurredAt, PayloadVersion: 1, Payload: payload}}})
}

func validateManagedResourceInput(resource ManagedResource) error {
	if resource.ResourceID == "" || len(resource.ResourceID) > 256 || resource.DisplayName == "" || len(resource.DisplayName) > 256 || resource.Purpose == "" || len(resource.Purpose) > 4096 || resource.MetadataSchemaVersion == "" || len(resource.MetadataSchemaVersion) > 64 {
		return newFailure(KindInvalidOperation, "create_managed_resource", "resource identity and descriptive fields are missing or unbounded", false, "supply bounded non-empty resource fields")
	}
	if resource.Class != "infrastructure" && resource.Class != "saas" {
		return newFailure(KindInvalidOperation, "create_managed_resource", "resource class is not recognized", false, "use infrastructure or saas")
	}
	if !validManagedResourceKind(resource.Kind) || !validStage(resource.StageMaturity, resource.StageAudienceCommitment) {
		return newFailure(KindInvalidOperation, "create_managed_resource", "resource kind or stage is not recognized", false, "use the closed C15 vocabularies")
	}
	if resource.LocatorAbsenceReason != "" && resource.LocatorAbsenceReason != "planned" && resource.LocatorAbsenceReason != "not_addressable" {
		return newFailure(KindInvalidOperation, "create_managed_resource", "locator absence reason is not recognized", false, "use planned or not_addressable")
	}
	if err := validateResourceEnvironments(resource.Environments, nil, nil); err != nil {
		return err
	}
	if len(resource.Metadata) > maxManagedResourceMetadataBytes || !isJSONObject(resource.Metadata) {
		return newFailure(KindInvalidOperation, "create_managed_resource", "metadata must be a JSON object within 16 KiB", false, "supply a JSON object of at most 16384 bytes")
	}
	if resource.Kind == "other" {
		var metadata map[string]json.RawMessage
		var detail string
		if err := json.Unmarshal(resource.Metadata, &metadata); err != nil || json.Unmarshal(metadata["kind_detail"], &detail) != nil || detail == "" || detail != strings.TrimSpace(detail) || len(detail) > maxManagedResourceKindDetail {
			return newFailure(KindInvalidOperation, "create_managed_resource", "other resources require kind_detail metadata", false, "supply bounded kind_detail metadata")
		}
	}
	return nil
}

func foldManagedResourceCreated(ctx context.Context, tx *sql.Tx, event Event) error {
	if err := checkSubject(event, SubjectProduct); err != nil {
		return err
	}
	var p managedResourceCreatedPayload
	if err := decodePayload(event, &p); err != nil {
		return err
	}
	if p.ProductID != event.SubjectID || p.ProductID == "" {
		return newFailure(KindInvalidPayload, "fold_event", "managed resource owner Product does not match event subject", false, "use the event Product as the resource owner")
	}
	if p.Environments == nil || p.OwnerEnvironments == nil {
		return newFailure(KindInvalidPayload, "fold_event", "managed resource environments must be explicit arrays", false, "supply resource and owner environments")
	}
	resource := ManagedResource{ResourceID: p.ResourceID, DisplayName: p.DisplayName, Class: p.Class, Kind: p.Kind, Purpose: p.Purpose, StageMaturity: p.StageMaturity, StageAudienceCommitment: p.StageAudienceCommitment, Environments: p.Environments, LocatorAbsenceReason: p.LocatorAbsenceReason, MetadataSchemaVersion: p.MetadataSchemaVersion, Metadata: p.Metadata, Version: 1}
	if err := validateManagedResourceInput(resource); err != nil {
		return err
	}
	if err := validateResourceEnvironments(p.OwnerEnvironments, p.Environments, nil); err != nil {
		return err
	}
	if p.OwnerPurpose == "" || len(p.OwnerPurpose) > 512 {
		return newFailure(KindInvalidPayload, "fold_event", "managed resource owner purpose is missing or unbounded", false, "supply a bounded owner purpose")
	}
	var productExists int
	if err := tx.QueryRowContext(ctx, `SELECT 1 FROM products WHERE id=?`, p.ProductID).Scan(&productExists); err == sql.ErrNoRows {
		return newFailure(KindProjectionNotFound, "fold_event", "resource owner Product does not exist", false, "create the Product before its resource")
	} else if err != nil {
		return wrapFailure(KindUnavailable, "fold_event", "cannot inspect resource owner Product", true, "retry once the database is readable", err)
	}
	_, err := tx.ExecContext(ctx, `INSERT INTO managed_resources(resource_id,display_name,class,kind,purpose,stage_maturity,stage_audience_commitment,environments,locator_absence_reason,metadata_schema_version,metadata,version,created_at,updated_at) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?)`, p.ResourceID, p.DisplayName, p.Class, p.Kind, p.Purpose, p.StageMaturity, p.StageAudienceCommitment, marshalStrings(p.Environments), nullString(p.LocatorAbsenceReason), p.MetadataSchemaVersion, string(p.Metadata), 1, event.OccurredAt.UTC().Format(time.RFC3339Nano), event.OccurredAt.UTC().Format(time.RFC3339Nano))
	if err != nil {
		if isUniqueViolation(err) {
			return newFailure(KindProjectionConflict, "fold_event", "managed resource identity already exists", false, "choose an unused resource ID")
		}
		return wrapFailure(KindUnavailable, "fold_event", "cannot write managed resource", true, "retry once the database is writable", err)
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO resource_products(resource_id,product_id,role,purpose,environments) VALUES(?,?,?,?,?)`, p.ResourceID, p.ProductID, "owner", p.OwnerPurpose, marshalStrings(p.OwnerEnvironments)); err != nil {
		return wrapFailure(KindUnavailable, "fold_event", "cannot write managed resource owner", true, "retry once the database is writable", err)
	}
	return nil
}

func foldManagedResourceConsumerAdded(ctx context.Context, tx *sql.Tx, event Event) error {
	if err := checkSubject(event, SubjectProduct); err != nil {
		return err
	}
	var p managedResourceConsumerAddedPayload
	if err := decodePayload(event, &p); err != nil {
		return err
	}
	if p.ProductID != event.SubjectID || p.ProductID == "" || p.ResourceID == "" || p.Purpose == "" || len(p.Purpose) > 512 || p.ExpectedVersion < 1 || p.ResultingVersion != p.ExpectedVersion+1 {
		return newFailure(KindInvalidPayload, "fold_event", "managed resource consumer payload is incomplete", false, "supply resource, Product, purpose, and consecutive versions")
	}
	if p.Environments == nil {
		return newFailure(KindInvalidPayload, "fold_event", "managed resource consumer environments must be an explicit array", false, "supply consumer environments")
	}
	var declared string
	if err := tx.QueryRowContext(ctx, `SELECT environments FROM managed_resources WHERE resource_id=? AND version=?`, p.ResourceID, p.ExpectedVersion).Scan(&declared); err == sql.ErrNoRows {
		return newFailure(KindVersionConflict, "fold_event", "managed resource is missing or has a different version", false, "reload the resource and retry with its current version")
	} else if err != nil {
		return wrapFailure(KindUnavailable, "fold_event", "cannot inspect managed resource version", true, "retry once the database is readable", err)
	}
	if err := validateResourceEnvironments(p.Environments, declared, nil); err != nil {
		return err
	}
	var exists int
	if err := tx.QueryRowContext(ctx, `SELECT 1 FROM products WHERE id=?`, p.ProductID).Scan(&exists); err == sql.ErrNoRows {
		return newFailure(KindProjectionNotFound, "fold_event", "consumer Product does not exist", false, "create the Product before adding the consumer")
	} else if err != nil {
		return wrapFailure(KindUnavailable, "fold_event", "cannot inspect consumer Product", true, "retry once the database is readable", err)
	}
	if err := tx.QueryRowContext(ctx, `SELECT 1 FROM resource_products WHERE resource_id=? AND product_id=?`, p.ResourceID, p.ProductID).Scan(&exists); err == nil {
		return newFailure(KindProjectionConflict, "fold_event", "Product is already linked to the managed resource", false, "use the existing owner or consumer link")
	} else if err != sql.ErrNoRows {
		return wrapFailure(KindUnavailable, "fold_event", "cannot inspect existing resource Product link", true, "retry once the database is readable", err)
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO resource_products(resource_id,product_id,role,purpose,environments) VALUES(?,?,?,?,?)`, p.ResourceID, p.ProductID, "consumer", p.Purpose, marshalStrings(p.Environments)); err != nil {
		if isUniqueViolation(err) {
			return newFailure(KindProjectionConflict, "fold_event", "Product is already linked to the managed resource", false, "use the existing resource link")
		}
		return wrapFailure(KindUnavailable, "fold_event", "cannot write managed resource consumer", true, "retry once the database is writable", err)
	}
	_, err := tx.ExecContext(ctx, `UPDATE managed_resources SET version=?,updated_at=? WHERE resource_id=? AND version=?`, p.ResultingVersion, event.OccurredAt.UTC().Format(time.RFC3339Nano), p.ResourceID, p.ExpectedVersion)
	if err != nil {
		return wrapFailure(KindUnavailable, "fold_event", "cannot advance managed resource version", true, "retry once the database is writable", err)
	}
	return nil
}

func validManagedResourceKind(kind string) bool {
	switch kind {
	case "service", "database", "queue", "job", "schedule", "runner_pool", "storage", "observability", "identity", "saas_account", "saas_project", "other":
		return true
	default:
		return false
	}
}

func validStage(maturity, audience string) bool {
	validMaturity := maturity == "prototype" || maturity == "alpha" || maturity == "beta" || maturity == "production" || maturity == "deprecated"
	validAudience := audience == "operator_only" || audience == "limited" || audience == "public"
	return validMaturity && validAudience
}

func validateResourceEnvironments(values []string, declared any, productDeclared any) error {
	seen := make(map[string]struct{}, len(values))
	for _, environment := range values {
		if environment != "development" && environment != "test" && environment != "preview" && environment != "staging" && environment != "production" && environment != "other" {
			return newFailure(KindInvalidOperation, "resource_environments", fmt.Sprintf("environment %q is not recognized", environment), false, "use the closed C15 environment vocabulary")
		}
		if _, exists := seen[environment]; exists {
			return newFailure(KindInvalidOperation, "resource_environments", "environment list contains a duplicate", false, "supply each environment once")
		}
		seen[environment] = struct{}{}
	}
	if len(values) > 16 {
		return newFailure(KindLimitExceeded, "resource_environments", "environment list exceeds 16 entries", false, "reduce the environment list")
	}
	for _, constraint := range []any{declared, productDeclared} {
		if constraint == nil {
			continue
		}
		allowed := make(map[string]struct{})
		var raw []byte
		switch value := constraint.(type) {
		case string:
			raw = []byte(value)
		case []byte:
			raw = value
		case []string:
			for _, item := range value {
				allowed[item] = struct{}{}
			}
		default:
			return newFailure(KindInvalidOperation, "resource_environments", "environment subset declaration has an unsupported type", false, "supply a JSON array")
		}
		if len(raw) > 0 {
			var declaredValues []string
			if err := json.Unmarshal(raw, &declaredValues); err != nil {
				return newFailure(KindInvalidOperation, "resource_environments", "declared environments are not a JSON array", false, "supply a JSON array")
			}
			for _, item := range declaredValues {
				allowed[item] = struct{}{}
			}
		}
		for _, item := range values {
			if _, ok := allowed[item]; !ok {
				return newFailure(KindInvalidRelation, "resource_environments", "attachment environment is outside the declared resource/Product environments", false, "use an environment declared by both endpoints")
			}
		}
	}
	return nil
}
