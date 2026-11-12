package ax

// Point, Size and Rect are AX geometry in points.
type (
	Point struct{ X, Y float64 }
	Size  struct{ W, H float64 }
	Rect  struct{ X, Y, W, H float64 }
	Range struct{ Location, Length int }
)

// URL is a CFURL's absolute string.
type URL string

// Contains reports whether a point lies inside r (right and bottom edges
// excluded).
func (r Rect) Contains(x, y float64) bool {
	return x >= r.X && y >= r.Y && x < r.X+r.W && y < r.Y+r.H
}
