package store

import (
	"fmt"
	"slices"
)

// CD-0229 D4: a versioned decision model sets each work item's size from
// recorded inputs. The same inputs and version always give the same size, an
// unknown version refuses rather than falling back, and no agent-proposed
// size is ever an input. The size never lowers a CD-0006 obligation band.

// WorkSizeModelVersion is the version a new size record is decided under.
const WorkSizeModelVersion = 2

// Blast radius: the widest thing a defect in the change could break.
const (
	BlastRadiusContained    = "contained"
	BlastRadiusPackage      = "package"
	BlastRadiusCrossPackage = "cross_package"
	BlastRadiusExternal     = "external"
)

const workSizeMaxProductionLines = 100000

// WorkSizeDeclared holds the inputs only the agent can supply. Independent
// review checks each one (CD-0229 D4).
type WorkSizeDeclared struct {
	Security                 bool   `json:"security"`
	UserVisible              bool   `json:"user_visible"`
	BlastRadius              string `json:"blast_radius"`
	EstimatedProductionLines int    `json:"estimated_production_lines"`
}

// WorkSizeInputs is the complete input record of one size decision. The
// counts derive from the footprint snapshot and the work's memberships; kind
// and stage derive from the store. Every field is a scalar, so two input
// records compare with ==.
type WorkSizeInputs struct {
	Kind              string           `json:"kind"`
	Maturity          string           `json:"maturity"`
	Audience          string           `json:"audience"`
	PathCount         int              `json:"path_count"`
	DomainCount       int              `json:"domain_count"`
	PackageCount      int              `json:"package_count"`
	UnmappedPathCount int              `json:"unmapped_path_count"`
	RepositoryCount   int              `json:"repository_count"`
	Declared          WorkSizeDeclared `json:"declared"`
}

var workSizeModels = map[int]func(WorkSizeInputs, []string) WorkSize{
	1: func(in WorkSizeInputs, _ []string) WorkSize { return decideWorkSizeV1(in) },
	2: decideWorkSizeV2,
}

// DecideWorkSize runs the named model version over validated inputs and the
// review basis: the proof events (CD-0229 D5) the record rests on. Version 1
// predates the basis and refuses one.
func DecideWorkSize(version int, inputs WorkSizeInputs, reviewBasis []string) (WorkSize, error) {
	model, ok := workSizeModels[version]
	if !ok {
		return "", newFailure(KindInvalidInput, "work_size", fmt.Sprintf("work-size model version %d is not defined", version), false, "decide the size under a defined model version")
	}
	if version == 1 && len(reviewBasis) != 0 {
		return "", newFailure(KindInvalidInput, "work_size", "work-size model version 1 takes no review basis", false, "decide a reviewed size under a later model version")
	}
	if err := validateWorkSizeInputs(inputs); err != nil {
		return "", err
	}
	return model(inputs, reviewBasis), nil
}

// decideWorkSizeV2 is version 1 plus CD-0229 D5: a change whose review
// proved it bigger is crucial, and stays crucial once proved.
func decideWorkSizeV2(in WorkSizeInputs, reviewBasis []string) WorkSize {
	if len(reviewBasis) != 0 {
		return WorkSizeCrucial
	}
	return decideWorkSizeV1(in)
}

// decideWorkSizeV1: the first rule that matches wins. No named path means
// the span is unknown, which sizes like an unmapped path.
func decideWorkSizeV1(in WorkSizeInputs) WorkSize {
	d := in.Declared
	switch {
	case d.Security,
		in.RepositoryCount >= 2,
		in.DomainCount >= 3,
		d.EstimatedProductionLines > 400,
		d.BlastRadius == BlastRadiusExternal && in.Audience == "public":
		return WorkSizeCrucial
	case d.UserVisible,
		in.DomainCount == 2,
		in.PackageCount >= 3,
		in.UnmappedPathCount > 0,
		in.PathCount == 0,
		d.EstimatedProductionLines > 60,
		d.BlastRadius == BlastRadiusCrossPackage || d.BlastRadius == BlastRadiusExternal,
		in.Maturity == "production" && in.Audience == "public":
		return WorkSizeLarger
	default:
		return WorkSizeSmall
	}
}

func validateWorkSizeInputs(in WorkSizeInputs) error {
	invalid := func(message string) error {
		return newFailure(KindInvalidInput, "work_size", message, false, "supply size inputs inside the model's bounds")
	}
	switch {
	case !WorkKindStored(in.Kind):
		return invalid("size inputs name an unstored work kind")
	case !slices.Contains([]string{"prototype", "alpha", "beta", "production", "deprecated"}, in.Maturity):
		return invalid("size inputs name an unknown maturity")
	case !slices.Contains([]string{"operator_only", "limited", "public"}, in.Audience):
		return invalid("size inputs name an unknown audience")
	case in.PathCount < 0 || in.DomainCount < 0 || in.PackageCount < 0 || in.UnmappedPathCount < 0:
		return invalid("size input counts must not be negative")
	case in.UnmappedPathCount > in.PathCount || in.PackageCount > in.PathCount:
		return invalid("size inputs count more packages or unmapped paths than named paths")
	case in.RepositoryCount < 1:
		return invalid("size inputs need at least one repository")
	}
	return validateWorkSizeDeclared(in.Declared)
}

func validateWorkSizeDeclared(d WorkSizeDeclared) error {
	invalid := func(message string) error {
		return newFailure(KindInvalidInput, "work_size", message, false, "supply size inputs inside the model's bounds")
	}
	switch {
	case !slices.Contains([]string{BlastRadiusContained, BlastRadiusPackage, BlastRadiusCrossPackage, BlastRadiusExternal}, d.BlastRadius):
		return invalid("size inputs name an unknown blast radius")
	case d.EstimatedProductionLines < 1 || d.EstimatedProductionLines > workSizeMaxProductionLines:
		return invalid("estimated production lines must be between 1 and 100000")
	}
	return nil
}
