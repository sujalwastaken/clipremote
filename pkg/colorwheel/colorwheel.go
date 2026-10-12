// Package colorwheel holds the geometry and color math behind the "natural
// value" wheel: a hue ring with an inscribed triangle whose saturated-hue
// corner slides up and down with the natural value (lightness) of the hue.
//
// It has no UI or I/O dependencies, so it is unit-testable and reusable by any
// front end.
//
// # Triangle model
//
// The triangle has three corners: white (top-left), black (bottom-left) and the
// fully saturated hue (right). A color is a barycentric mix of the three,
//
//	rgb = wHue*pureHue + wWhite*white + wBlack*black
//
// which is exactly the classic HSV triangle (V = wHue+wWhite, S = wHue/V).
// What is unusual is where the hue corner sits: instead of a fixed mid-height
// position, its height is the hue's natural value, so yellow's corner rides up
// next to white and blue's drops toward black. With the default luma metric the
// vertical position of *every* point in the triangle is that color's Rec.709
// luma, because luma is linear in RGB and therefore interpolates exactly under
// barycentric mixing.
//
// # Ring orientation
//
// The ring places blue (240°) at the bottom, so the brightest hues (yellow)
// end up at the top, mirroring the triangle's vertical value axis.
package colorwheel

import (
	"fmt"
	"math"
	"strings"
)

// ---------------------------------------------------------------------------
// Natural-value metrics
// ---------------------------------------------------------------------------

// Metric selects how the "natural value" of a pure hue is measured.
type Metric int

const (
	// MetricLuma uses Rec.709 luma of the gamma-encoded pure hue. The triangle's
	// vertical axis then equals the luma of the picked color exactly.
	MetricLuma Metric = iota
	// MetricLab uses CIE L* (perceptual lightness) of the pure hue.
	MetricLab
	// MetricFlat ignores the hue: the corner stays at mid-height (classic look).
	MetricFlat
)

func (m Metric) String() string {
	switch m {
	case MetricLuma:
		return "luma"
	case MetricLab:
		return "lab"
	case MetricFlat:
		return "flat"
	}
	return "unknown"
}

// Next cycles luma -> lab -> flat -> luma.
func (m Metric) Next() Metric { return (m + 1) % 3 }

// ParseMetric parses "luma", "lab" or "flat" (case-insensitive).
func ParseMetric(s string) (Metric, error) {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "", "luma":
		return MetricLuma, nil
	case "lab", "lstar", "l*":
		return MetricLab, nil
	case "flat", "none", "off":
		return MetricFlat, nil
	}
	return MetricLuma, fmt.Errorf("unknown metric %q (want luma, lab or flat)", s)
}

// ---------------------------------------------------------------------------
// HSV <-> RGB (all channels 0..1, hue in degrees)
// ---------------------------------------------------------------------------

// HSV is a color in the HSV model: H in degrees [0,360), S and V in [0,1].
type HSV struct{ H, S, V float64 }

// NormHue wraps h into [0,360).
func NormHue(h float64) float64 {
	h = math.Mod(h, 360)
	if h < 0 {
		h += 360
	}
	if h >= 360 {
		h = 0
	}
	return h
}

// RGB converts to RGB with channels in [0,1].
func (c HSV) RGB() (r, g, b float64) {
	h := NormHue(c.H)
	s, v := clamp01(c.S), clamp01(c.V)
	ch := v * s
	hp := h / 60
	x := ch * (1 - math.Abs(math.Mod(hp, 2)-1))
	m := v - ch
	switch int(hp) % 6 {
	case 0:
		r, g, b = ch, x, 0
	case 1:
		r, g, b = x, ch, 0
	case 2:
		r, g, b = 0, ch, x
	case 3:
		r, g, b = 0, x, ch
	case 4:
		r, g, b = x, 0, ch
	default:
		r, g, b = ch, 0, x
	}
	return r + m, g + m, b + m
}

// FromRGB converts RGB (channels in [0,1]) to HSV. Hue is 0 for achromatic input.
func FromRGB(r, g, b float64) HSV {
	mx := math.Max(r, math.Max(g, b))
	mn := math.Min(r, math.Min(g, b))
	d := mx - mn
	out := HSV{V: mx}
	if mx > 0 {
		out.S = d / mx
	}
	if d == 0 {
		return out
	}
	switch mx {
	case r:
		out.H = 60 * math.Mod((g-b)/d, 6)
	case g:
		out.H = 60 * ((b-r)/d + 2)
	default:
		out.H = 60 * ((r-g)/d + 4)
	}
	out.H = NormHue(out.H)
	return out
}

// HueToRGB returns the fully saturated color (S=V=1) of hue h.
func HueToRGB(h float64) (r, g, b float64) { return HSV{H: h, S: 1, V: 1}.RGB() }

// To8 converts a [0,1] channel to a byte.
func To8(v float64) uint8 { return uint8(math.Round(clamp01(v) * 255)) }

// Luma is the Rec.709 luma of gamma-encoded RGB.
func Luma(r, g, b float64) float64 { return 0.2126*r + 0.7152*g + 0.0722*b }

// LabL is CIE L* (0..100) of an sRGB color.
func LabL(r, g, b float64) float64 {
	lin := func(c float64) float64 {
		if c <= 0.04045 {
			return c / 12.92
		}
		return math.Pow((c+0.055)/1.055, 2.4)
	}
	y := 0.2126729*lin(r) + 0.7151522*lin(g) + 0.0721750*lin(b)
	var f float64
	if y > 216.0/24389.0 {
		f = math.Cbrt(y)
	} else {
		f = (24389.0/27.0*y + 16) / 116
	}
	return 116*f - 16
}

// NaturalValue returns the natural value of the pure hue h in [0,1]
// (0 = black, 1 = white).
func NaturalValue(h float64, m Metric) float64 {
	r, g, b := HueToRGB(h)
	switch m {
	case MetricLuma:
		return clamp01(Luma(r, g, b))
	case MetricLab:
		return clamp01(LabL(r, g, b) / 100)
	}
	return 0.5
}

// ---------------------------------------------------------------------------
// Geometry
// ---------------------------------------------------------------------------

// Pt is a point in screen space (x right, y down).
type Pt struct{ X, Y float64 }

// BottomHue is the hue shown at the bottom (6 o'clock) of the ring.
const BottomHue = 240.0

// Region identifies which part of the wheel a point falls on.
type Region int

const (
	RegionNone Region = iota
	RegionRing
	RegionTriangle
)

// Layout is the wheel geometry for one window size.
type Layout struct {
	CX, CY float64 // wheel center
	OuterR float64 // outer radius of the hue ring
	InnerR float64 // inner radius of the hue ring
	TriR   float64 // radius of the circle the triangle is inscribed in
	HueDir float64 // +1: hue increases clockwise on screen, -1: counter-clockwise
}

// NewLayout builds a layout for a wheel of the given outer radius.
func NewLayout(cx, cy, outerR float64, ccw bool) Layout {
	dir := 1.0
	if ccw {
		dir = -1
	}
	return Layout{
		CX: cx, CY: cy,
		OuterR: outerR,
		InnerR: outerR * 0.82,
		TriR:   outerR * 0.76,
		HueDir: dir,
	}
}

// HueToPoint returns the point at distance r from the center in the direction
// of hue h (blue straight down, yellow straight up).
func (l Layout) HueToPoint(h, r float64) Pt {
	phi := (h - BottomHue) * l.HueDir * math.Pi / 180 // clockwise from the bottom
	return Pt{l.CX - r*math.Sin(phi), l.CY + r*math.Cos(phi)}
}

// PointToHue is the inverse of HueToPoint's direction (distance is ignored).
func (l Layout) PointToHue(p Pt) float64 {
	phi := math.Atan2(-(p.X-l.CX), p.Y-l.CY) * 180 / math.Pi
	return NormHue(BottomHue + phi*l.HueDir)
}

// Corners returns the triangle corners for natural value n: the saturated-hue
// corner, the white corner (top-left) and the black corner (bottom-left). The
// hue corner always lies on the TriR circle; its height spans the full white-to-
// black range as n goes from 1 to 0.
func (l Layout) Corners(n float64) (hue, white, black Pt) {
	n = clamp01(n)
	half := l.TriR * math.Sqrt(3) / 2
	white = Pt{l.CX - l.TriR/2, l.CY - half}
	black = Pt{l.CX - l.TriR/2, l.CY + half}
	dy := half * (1 - 2*n)
	dx := math.Sqrt(math.Max(0, l.TriR*l.TriR-dy*dy))
	hue = Pt{l.CX + dx, l.CY + dy}
	return
}

// SVToPoint maps saturation/value to a point inside the triangle for natural
// value n (barycentric: wHue=S*V, wWhite=V*(1-S), wBlack=1-V).
func (l Layout) SVToPoint(n, s, v float64) Pt {
	ph, pw, pb := l.Corners(n)
	s, v = clamp01(s), clamp01(v)
	wh, ww, wb := s*v, v*(1-s), 1-v
	return Pt{
		wh*ph.X + ww*pw.X + wb*pb.X,
		wh*ph.Y + ww*pw.Y + wb*pb.Y,
	}
}

// PointToSV maps a point to saturation/value, clamping points outside the
// triangle to its nearest boundary point. At the black corner saturation is
// undefined, so prevS is returned.
func (l Layout) PointToSV(n float64, p Pt, prevS float64) (s, v float64) {
	ph, pw, pb := l.Corners(n)
	q := ClosestInTriangle(p, ph, pw, pb)
	wh, ww, wb := Barycentric(q, ph, pw, pb)
	wh, ww, wb = math.Max(0, wh), math.Max(0, ww), math.Max(0, wb)
	if sum := wh + ww + wb; sum > 0 {
		wh, ww, wb = wh/sum, ww/sum, wb/sum
	}
	v = clamp01(wh + ww)
	if v < 1e-9 {
		return clamp01(prevS), 0
	}
	return clamp01(wh / v), v
}

// Height is p's normalized height inside the triangle: 0 at the black corner's
// level, 1 at the white corner's level. With MetricLuma this equals the luma of
// the color at p.
func (l Layout) Height(p Pt, n float64) float64 {
	_, pw, pb := l.Corners(n)
	return (pb.Y - p.Y) / (pb.Y - pw.Y)
}

// Hit reports which part of the wheel p falls on (n is the current natural
// value, which determines the triangle's shape).
func (l Layout) Hit(p Pt, n float64) Region {
	ph, pw, pb := l.Corners(n)
	wa, wb, wc := Barycentric(p, ph, pw, pb)
	if wa >= 0 && wb >= 0 && wc >= 0 {
		return RegionTriangle
	}
	d := math.Hypot(p.X-l.CX, p.Y-l.CY)
	slop := 0.03 * l.OuterR
	if d >= l.InnerR-slop && d <= l.OuterR+slop {
		return RegionRing
	}
	return RegionNone
}

// ---------------------------------------------------------------------------
// Triangle helpers
// ---------------------------------------------------------------------------

// Barycentric returns p's weights (wa, wb, wc) with respect to triangle abc.
func Barycentric(p, a, b, c Pt) (wa, wb, wc float64) {
	v0 := Pt{b.X - a.X, b.Y - a.Y}
	v1 := Pt{c.X - a.X, c.Y - a.Y}
	v2 := Pt{p.X - a.X, p.Y - a.Y}
	d00 := v0.X*v0.X + v0.Y*v0.Y
	d01 := v0.X*v1.X + v0.Y*v1.Y
	d11 := v1.X*v1.X + v1.Y*v1.Y
	d20 := v2.X*v0.X + v2.Y*v0.Y
	d21 := v2.X*v1.X + v2.Y*v1.Y
	den := d00*d11 - d01*d01
	if den == 0 {
		return 1, 0, 0
	}
	wb = (d11*d20 - d01*d21) / den
	wc = (d00*d21 - d01*d20) / den
	return 1 - wb - wc, wb, wc
}

// ClosestInTriangle returns p if it is inside triangle abc, otherwise the
// nearest point on the triangle's boundary.
func ClosestInTriangle(p, a, b, c Pt) Pt {
	wa, wb, wc := Barycentric(p, a, b, c)
	if wa >= 0 && wb >= 0 && wc >= 0 {
		return p
	}
	best := closestOnSegment(p, a, b)
	bd := dist2(p, best)
	for _, seg := range [2][2]Pt{{b, c}, {c, a}} {
		q := closestOnSegment(p, seg[0], seg[1])
		if d := dist2(p, q); d < bd {
			best, bd = q, d
		}
	}
	return best
}

func closestOnSegment(p, a, b Pt) Pt {
	abx, aby := b.X-a.X, b.Y-a.Y
	den := abx*abx + aby*aby
	if den == 0 {
		return a
	}
	t := clamp01(((p.X-a.X)*abx + (p.Y-a.Y)*aby) / den)
	return Pt{a.X + t*abx, a.Y + t*aby}
}

func dist2(a, b Pt) float64 {
	dx, dy := a.X-b.X, a.Y-b.Y
	return dx*dx + dy*dy
}

func clamp01(v float64) float64 {
	if v < 0 {
		return 0
	}
	if v > 1 {
		return 1
	}
	return v
}