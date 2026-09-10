package launcher

// Surface identifies the data context shown by the launcher pane layout.
type Surface string

type Screen = Surface

const (
	SurfacePortfolio Surface = "portfolio"
	SurfaceProduct   Surface = "product"
	SurfaceWork      Surface = "work"

	// Screen names identify surface values used by the read port and view.
	ScreenPortfolio = SurfacePortfolio
	ScreenProduct   = SurfaceProduct
	ScreenWork      = SurfaceWork
)
