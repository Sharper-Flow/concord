package agent

import (
	"fmt"
	"strconv"
	"strings"
)

type SemVer struct{ Major, Minor, Patch int }

func ParseSemVer(value string) (SemVer, error) {
	var out SemVer
	parts := strings.Split(value, ".")
	if len(parts) != 3 || value == "" {
		return out, fmt.Errorf("invalid semantic version %q", value)
	}
	parsed := []*int{&out.Major, &out.Minor, &out.Patch}
	for i, part := range parts {
		if part == "" || (len(part) > 1 && part[0] == '0') {
			return out, fmt.Errorf("invalid semantic version %q", value)
		}
		n, err := strconv.Atoi(part)
		if err != nil || n < 0 {
			return out, fmt.Errorf("invalid semantic version %q", value)
		}
		*parsed[i] = n
	}
	return out, nil
}
func (v SemVer) String() string { return fmt.Sprintf("%d.%d.%d", v.Major, v.Minor, v.Patch) }
func (v SemVer) Compare(other SemVer) int {
	if v.Major != other.Major {
		if v.Major < other.Major {
			return -1
		}
		return 1
	}
	if v.Minor != other.Minor {
		if v.Minor < other.Minor {
			return -1
		}
		return 1
	}
	if v.Patch < other.Patch {
		return -1
	}
	if v.Patch > other.Patch {
		return 1
	}
	return 0
}

type VersionRange struct{ Min, Max SemVer }

func ParseVersionRange(value string) (VersionRange, error) {
	parts := strings.Split(value, "-")
	if len(parts) > 2 {
		return VersionRange{}, fmt.Errorf("invalid version range %q", value)
	}
	min, err := ParseSemVer(strings.TrimSpace(parts[0]))
	if err != nil {
		return VersionRange{}, err
	}
	max := min
	if len(parts) == 2 {
		max, err = ParseSemVer(strings.TrimSpace(parts[1]))
		if err != nil {
			return VersionRange{}, err
		}
	}
	if min.Compare(max) > 0 {
		return VersionRange{}, fmt.Errorf("descending version range %q", value)
	}
	return VersionRange{Min: min, Max: max}, nil
}
func (r VersionRange) Contains(v SemVer) bool { return r.Min.Compare(v) <= 0 && r.Max.Compare(v) >= 0 }
func NegotiateSurfaceVersion(supportedRange string) (string, error) {
	r, err := ParseVersionRange(supportedRange)
	if err != nil {
		return "", err
	}
	supported := []SemVer{{Major: 1, Minor: 0, Patch: 0}}
	var selected *SemVer
	for _, candidate := range supported {
		if r.Contains(candidate) && (selected == nil || candidate.Compare(*selected) > 0) {
			copy := candidate
			selected = &copy
		}
	}
	if selected == nil {
		return "", fmt.Errorf("no compatible surface version")
	}
	return selected.String(), nil
}
