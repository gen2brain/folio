package raster

// Cap is how a stroke ends, matching the PDF J operator.
type Cap int

// Line caps.
const (
	CapButt Cap = iota
	CapRound
	CapSquare
	// CapTriangle has no PDF operator and no dot: it is here because other
	// formats have it and the stroker costs nothing to make it work.
	CapTriangle
)

// Join is how two segments meet, matching the PDF j operator.
type Join int

// Line joins.
const (
	JoinMiter Join = iota
	JoinRound
	JoinBevel
)

// Stroke is the state the w, J, j, M and d operators set.
type Stroke struct {
	Width      float32
	MiterLimit float32
	StartCap   Cap
	DashCap    Cap
	EndCap     Cap
	Join       Join
	DashPhase  float32
	Dash       []float32
}

// DefaultStroke is the state a content stream starts with, ISO 32000-1 table 52.
func DefaultStroke() Stroke {
	return Stroke{Width: 1, MiterLimit: 10}
}

// Clone returns a copy that shares no dash array with the original.
func (s *Stroke) Clone() *Stroke {
	out := *s
	out.Dash = append([]float32(nil), s.Dash...)
	return &out
}

// SetCaps sets all three caps, which is all the PDF J operator can express.
func (s *Stroke) SetCaps(c Cap) { s.StartCap, s.DashCap, s.EndCap = c, c, c }
