package predecessor

// The parallel migration mode's surface vocabulary. CD-0097 D2 names exactly
// five predecessor surfaces the mode covers, each with one authority while
// both systems stay writable. This closed set is the single authority for
// what a migration run can name: the preflight inventory enumerates it, and
// the import verb validates requests against it. A surface outside this set
// is outside the accepted mode and refuses before import starts.
const (
	SurfaceSpecifications  = "specifications"
	SurfaceActiveWork      = "active_work"
	SurfaceTerminalHistory = "terminal_history"
	SurfaceWisdom          = "wisdom"
	SurfaceReflections     = "reflections"
)

// SurfaceInclusion values reported by the preflight inventory. A surface is
// included when the import verb moves it, and excluded when it migrates by
// its own route or stays captured in the snapshot.
const (
	InclusionIncluded = "included"
	InclusionExcluded = "excluded"
)

// SurfaceRoute is one surface's standing under CD-0097 D2: whether the import
// verb moves it, the route it takes instead when it does not, and the
// structural capture gap the snapshot contract carries for it.
type SurfaceRoute struct {
	Surface    string
	Importable bool
	Route      string
	CaptureGap string
}

// surfaceRoutes is the closed, deterministically ordered route table. The
// order is fixed so the preflight inventory and import refusals enumerate
// every surface identically across runs.
var surfaceRoutes = []SurfaceRoute{
	{
		Surface:    SurfaceSpecifications,
		Importable: false,
		Route:      "arrives as source material; a specification becomes Product law only through the knowledge procedure",
		CaptureGap: "the sanctioned harvest does not capture specifications",
	},
	{
		Surface:    SurfaceActiveWork,
		Importable: true,
		Route:      "imports through `predecessor import` under the existing safeguards; the predecessor keeps authority until cutover",
	},
	{
		Surface:    SurfaceTerminalHistory,
		Importable: false,
		Route:      "stays captured in the validated snapshot; importing it as a read-only record is the mode's bounded extension",
		CaptureGap: "totals only; per-change terminal entries are not captured",
	},
	{
		Surface:    SurfaceWisdom,
		Importable: false,
		Route:      "migrates as curation input under the knowledge-formalization procedure; every drop records a reason",
	},
	{
		Surface:    SurfaceReflections,
		Importable: false,
		Route:      "migrate as research source material under the same provenance rule as wisdom",
	},
}

// ModeSurfaceNames returns the mode's surface names in route-table order.
func ModeSurfaceNames() []string {
	names := make([]string, 0, len(surfaceRoutes))
	for _, route := range surfaceRoutes {
		names = append(names, route.Surface)
	}
	return names
}

// SurfaceRouteFor returns the route entry for a mode surface. The second
// return is false when the surface is outside the mode.
func SurfaceRouteFor(surface string) (SurfaceRoute, bool) {
	for _, route := range surfaceRoutes {
		if route.Surface == surface {
			return route, true
		}
	}
	return SurfaceRoute{}, false
}
