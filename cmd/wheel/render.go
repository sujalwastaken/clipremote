package main

import (
	"math"

	"github.com/chocolatkey/clipremote/pkg/colorwheel"
)

// The wheel is rasterized on the CPU into an opaque RGBA buffer (anti-aliased
// ring, triangle and markers) and uploaded to a single ebiten image. The ring is
// cached per window size, the triangle per (hue, natural value), so dragging
// inside the triangle only redraws the markers.

type status int

const (
	statusOffline status = iota // UI-only mode, no CSP connection requested
	statusDown                  // connection requested but not alive
	statusUp                    // connected and authenticated
)

type color3 [3]float64

var (
	colBG       = color3{0.133, 0.133, 0.149}
	colFooter   = color3{0.098, 0.098, 0.114}
	colWhite    = color3{1, 1, 1}
	colDark     = color3{0.05, 0.05, 0.06}
	colUp       = color3{0.20, 0.83, 0.60}
	colDown     = color3{0.97, 0.44, 0.44}
	colOffline  = color3{0.50, 0.50, 0.56}
	footerLogic = 30.0 // footer height in device-independent pixels
)

type renderKey struct {
	w, h   int
	scale  float64
	dir    float64
	hsv    colorwheel.HSV
	metric colorwheel.Metric
	st     status
}

type renderer struct {
	w, h, wheelH int
	scale, dir   float64
	lay          colorwheel.Layout

	ring, base, out []byte // ring only / ring+triangle / final frame (all opaque RGBA)
	baseHue, baseN  float64
	baseOK          bool

	last   renderKey
	lastOK bool
}

// footerHeight is the footer height in physical pixels for a given scale.
func footerHeight(scale float64) int { return int(math.Round(footerLogic * scale)) }

// setSize (re)computes the layout and the cached ring if anything changed.
func (r *renderer) setSize(w, h int, scale, dir float64) {
	if w < 1 || h < 1 {
		return
	}
	if w == r.w && h == r.h && scale == r.scale && dir == r.dir {
		return
	}
	r.w, r.h, r.scale, r.dir = w, h, scale, dir
	footer := footerHeight(scale)
	r.wheelH = h - footer
	if r.wheelH < 1 {
		r.wheelH = h
	}
	side := math.Min(float64(w), float64(r.wheelH))
	outer := side/2 - 6*scale
	if outer < 20 {
		outer = 20
	}
	r.lay = colorwheel.NewLayout(float64(w)/2, float64(r.wheelH)/2, outer, dir < 0)

	n := w * h * 4
	r.ring, r.base, r.out = make([]byte, n), make([]byte, n), make([]byte, n)
	r.buildRing()
	r.baseOK, r.lastOK = false, false
}

// render returns the finished frame and whether it differs from the last one.
func (r *renderer) render(hsv colorwheel.HSV, metric colorwheel.Metric, st status) ([]byte, bool) {
	if r.w == 0 {
		return nil, false
	}
	key := renderKey{r.w, r.h, r.scale, r.dir, hsv, metric, st}
	if r.lastOK && key == r.last {
		return r.out, false
	}
	n := colorwheel.NaturalValue(hsv.H, metric)
	if !r.baseOK || hsv.H != r.baseHue || n != r.baseN {
		copy(r.base, r.ring)
		r.drawTriangle(r.base, hsv.H, n)
		r.baseHue, r.baseN, r.baseOK = hsv.H, n, true
	}
	copy(r.out, r.base)
	r.drawMarkers(r.out, hsv, n)
	cr, cg, cb := hsv.RGB()
	r.drawFooter(r.out, color3{cr, cg, cb}, st)
	r.last, r.lastOK = key, true
	return r.out, true
}

// ---------------------------------------------------------------------------
// Rasterization
// ---------------------------------------------------------------------------

func (r *renderer) buildRing() {
	for i := 0; i < len(r.ring); i += 4 {
		r.ring[i], r.ring[i+1], r.ring[i+2], r.ring[i+3] =
			colorwheel.To8(colBG[0]), colorwheel.To8(colBG[1]), colorwheel.To8(colBG[2]), 255
	}
	l := r.lay
	x0, x1 := int(l.CX-l.OuterR)-2, int(l.CX+l.OuterR)+2
	y0, y1 := int(l.CY-l.OuterR)-2, int(l.CY+l.OuterR)+2
	for y := y0; y <= y1; y++ {
		for x := x0; x <= x1; x++ {
			px, py := float64(x)+0.5, float64(y)+0.5
			d := math.Hypot(px-l.CX, py-l.CY)
			a := clamp01(d-l.InnerR+0.5) * clamp01(l.OuterR-d+0.5)
			if a <= 0 {
				continue
			}
			cr, cg, cb := colorwheel.HueToRGB(l.PointToHue(colorwheel.Pt{X: px, Y: py}))
			r.put(r.ring, x, y, cr, cg, cb, a)
		}
	}
}

func (r *renderer) drawTriangle(buf []byte, hue, n float64) {
	l := r.lay
	ph, pw, pb := l.Corners(n)
	hr, hg, hb := colorwheel.HueToRGB(hue)

	// Altitudes (distance from each corner to its opposite edge) turn barycentric
	// weights into pixel distances for edge anti-aliasing.
	area2 := math.Abs((pw.X-ph.X)*(pb.Y-ph.Y) - (pb.X-ph.X)*(pw.Y-ph.Y))
	altH := area2 / math.Hypot(pw.X-pb.X, pw.Y-pb.Y)
	altW := area2 / math.Hypot(ph.X-pb.X, ph.Y-pb.Y)
	altB := area2 / math.Hypot(ph.X-pw.X, ph.Y-pw.Y)

	x0 := int(math.Floor(math.Min(ph.X, math.Min(pw.X, pb.X)))) - 1
	x1 := int(math.Ceil(math.Max(ph.X, math.Max(pw.X, pb.X)))) + 1
	y0 := int(math.Floor(math.Min(ph.Y, math.Min(pw.Y, pb.Y)))) - 1
	y1 := int(math.Ceil(math.Max(ph.Y, math.Max(pw.Y, pb.Y)))) + 1
	for y := y0; y <= y1; y++ {
		for x := x0; x <= x1; x++ {
			p := colorwheel.Pt{X: float64(x) + 0.5, Y: float64(y) + 0.5}
			wh, ww, wb := colorwheel.Barycentric(p, ph, pw, pb)
			d := math.Min(wh*altH, math.Min(ww*altW, wb*altB))
			a := clamp01(d + 0.5)
			if a <= 0 {
				continue
			}
			wh, ww, wb = math.Max(0, wh), math.Max(0, ww), math.Max(0, wb)
			sum := wh + ww + wb
			wh, ww, wb = wh/sum, ww/sum, wb/sum
			r.put(buf, x, y, wh*hr+ww, wh*hg+ww, wh*hb+ww, a) // black corner contributes 0
		}
	}
}

func (r *renderer) drawMarkers(buf []byte, hsv colorwheel.HSV, n float64) {
	l := r.lay
	stroke := math.Max(1.5, 1.6*r.scale)
	edge := 2 * r.scale

	ringW := l.OuterR - l.InnerR
	rp := l.HueToPoint(hsv.H, (l.InnerR+l.OuterR)/2)
	rr := math.Max(ringW*0.36, 4*r.scale)
	r.strokeCircle(buf, rp.X, rp.Y, rr, stroke+edge, colDark)
	r.strokeCircle(buf, rp.X, rp.Y, rr, stroke, colWhite)

	tp := l.SVToPoint(n, hsv.S, hsv.V)
	tr := math.Max(l.OuterR*0.045, 5*r.scale)
	r.strokeCircle(buf, tp.X, tp.Y, tr, stroke+edge, colDark)
	r.strokeCircle(buf, tp.X, tp.Y, tr, stroke, colWhite)
}

func (r *renderer) drawFooter(buf []byte, cur color3, st status) {
	r.fillRect(buf, 0, r.wheelH, r.w, r.h, colFooter)
	pad := int(math.Round(6 * r.scale))
	dotArea := int(math.Round(26 * r.scale))
	sx0, sx1 := pad, r.w-pad-dotArea
	sy0, sy1 := r.wheelH+pad/2+1, r.h-pad/2-1
	r.fillRect(buf, sx0-1, sy0-1, sx1+1, sy1+1, colDark)
	r.fillRect(buf, sx0, sy0, sx1, sy1, cur)

	c := colOffline
	switch st {
	case statusUp:
		c = colUp
	case statusDown:
		c = colDown
	}
	r.fillCircle(buf, float64(r.w-pad-dotArea/2)-2*r.scale, float64(r.wheelH)+float64(r.h-r.wheelH)/2, 5*r.scale, c)
}

// ---------------------------------------------------------------------------
// Pixel helpers
// ---------------------------------------------------------------------------

// put alpha-blends (cr,cg,cb) at pixel (x,y) of an opaque buffer.
func (r *renderer) put(buf []byte, x, y int, cr, cg, cb, a float64) {
	if x < 0 || y < 0 || x >= r.w || y >= r.h || a <= 0 {
		return
	}
	i := (y*r.w + x) * 4
	if a >= 1 {
		buf[i], buf[i+1], buf[i+2], buf[i+3] = colorwheel.To8(cr), colorwheel.To8(cg), colorwheel.To8(cb), 255
		return
	}
	mix := func(dst byte, src float64) byte {
		return colorwheel.To8(float64(dst)/255*(1-a) + src*a)
	}
	buf[i], buf[i+1], buf[i+2], buf[i+3] = mix(buf[i], cr), mix(buf[i+1], cg), mix(buf[i+2], cb), 255
}

func (r *renderer) fillRect(buf []byte, x0, y0, x1, y1 int, c color3) {
	for y := y0; y < y1; y++ {
		for x := x0; x < x1; x++ {
			r.put(buf, x, y, c[0], c[1], c[2], 1)
		}
	}
}

func (r *renderer) fillCircle(buf []byte, cx, cy, rad float64, c color3) {
	for y := int(cy - rad - 2); y <= int(cy+rad+2); y++ {
		for x := int(cx - rad - 2); x <= int(cx+rad+2); x++ {
			d := math.Hypot(float64(x)+0.5-cx, float64(y)+0.5-cy)
			r.put(buf, x, y, c[0], c[1], c[2], clamp01(rad-d+0.5))
		}
	}
}

// strokeCircle draws an anti-aliased circle outline centered on radius rad.
func (r *renderer) strokeCircle(buf []byte, cx, cy, rad, thick float64, c color3) {
	ext := rad + thick/2 + 2
	for y := int(cy - ext); y <= int(cy+ext); y++ {
		for x := int(cx - ext); x <= int(cx+ext); x++ {
			d := math.Hypot(float64(x)+0.5-cx, float64(y)+0.5-cy)
			r.put(buf, x, y, c[0], c[1], c[2], clamp01(thick/2+0.5-math.Abs(d-rad)))
		}
	}
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