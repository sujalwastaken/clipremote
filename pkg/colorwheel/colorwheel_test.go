package colorwheel

import (
	"math"
	"math/rand"
	"testing"
)

func near(a, b, eps float64) bool { return math.Abs(a-b) <= eps }

func hueDiff(a, b float64) float64 {
	d := math.Abs(NormHue(a) - NormHue(b))
	return math.Min(d, 360-d)
}

func TestHSVRoundTrip(t *testing.T) {
	rng := rand.New(rand.NewSource(1))
	for i := 0; i < 2000; i++ {
		c := HSV{H: rng.Float64() * 360, S: 0.01 + rng.Float64()*0.99, V: 0.01 + rng.Float64()*0.99}
		r, g, b := c.RGB()
		got := FromRGB(r, g, b)
		if hueDiff(got.H, c.H) > 1e-6 || !near(got.S, c.S, 1e-9) || !near(got.V, c.V, 1e-9) {
			t.Fatalf("round trip %+v -> %+v", c, got)
		}
	}
}

func TestPureHues(t *testing.T) {
	cases := []struct {
		h       float64
		r, g, b float64
	}{
		{0, 1, 0, 0}, {60, 1, 1, 0}, {120, 0, 1, 0}, {180, 0, 1, 1}, {240, 0, 0, 1}, {300, 1, 0, 1},
	}
	for _, c := range cases {
		r, g, b := HueToRGB(c.h)
		if !near(r, c.r, 1e-9) || !near(g, c.g, 1e-9) || !near(b, c.b, 1e-9) {
			t.Errorf("HueToRGB(%v) = %v,%v,%v", c.h, r, g, b)
		}
	}
	if NormHue(-1e-17) != 0 && NormHue(-1e-17) >= 360 {
		t.Error("NormHue must stay in [0,360)")
	}
}

func TestNaturalValue(t *testing.T) {
	luma := map[float64]float64{0: 0.2126, 60: 0.9278, 120: 0.7152, 180: 0.7874, 240: 0.0722, 300: 0.2848}
	for h, want := range luma {
		if got := NaturalValue(h, MetricLuma); !near(got, want, 1e-4) {
			t.Errorf("luma(%v) = %v, want %v", h, got, want)
		}
	}
	if y, b := NaturalValue(60, MetricLab), NaturalValue(240, MetricLab); !(y > 0.95 && b < 0.35 && b > 0.3) {
		t.Errorf("lab yellow=%v blue=%v", y, b)
	}
	for h := 0.0; h < 360; h += 7 {
		if got := NaturalValue(h, MetricFlat); got != 0.5 {
			t.Fatalf("flat(%v) = %v", h, got)
		}
		for _, m := range []Metric{MetricLuma, MetricLab} {
			if n := NaturalValue(h, m); n < 0 || n > 1 {
				t.Fatalf("%v(%v) = %v out of range", m, h, n)
			}
		}
	}
}

func TestParseMetric(t *testing.T) {
	for in, want := range map[string]Metric{"": MetricLuma, "LUMA": MetricLuma, "lab": MetricLab, " flat ": MetricFlat} {
		if got, err := ParseMetric(in); err != nil || got != want {
			t.Errorf("ParseMetric(%q) = %v, %v", in, got, err)
		}
	}
	if _, err := ParseMetric("nope"); err == nil {
		t.Error("expected error")
	}
	if MetricLuma.Next() != MetricLab || MetricLab.Next() != MetricFlat || MetricFlat.Next() != MetricLuma {
		t.Error("Next cycle broken")
	}
}

// With the luma metric, the height of a color inside the triangle IS its luma.
func TestTriangleHeightIsLuma(t *testing.T) {
	l := NewLayout(200, 200, 150, false)
	rng := rand.New(rand.NewSource(2))
	for i := 0; i < 2000; i++ {
		c := HSV{H: rng.Float64() * 360, S: rng.Float64(), V: rng.Float64()}
		n := NaturalValue(c.H, MetricLuma)
		p := l.SVToPoint(n, c.S, c.V)
		r, g, b := c.RGB()
		if got, want := l.Height(p, n), Luma(r, g, b); !near(got, want, 1e-9) {
			t.Fatalf("%+v: height %v, luma %v", c, got, want)
		}
	}
}

func TestHueCornerMovesWithNaturalValue(t *testing.T) {
	l := NewLayout(0, 0, 100, false)
	_, _, _ = l.Corners(0.5)
	yh, _, _ := l.Corners(NaturalValue(60, MetricLuma))  // yellow
	bh, _, _ := l.Corners(NaturalValue(240, MetricLuma)) // blue
	if !(yh.Y < bh.Y) {
		t.Errorf("yellow corner (y=%v) should be above blue corner (y=%v)", yh.Y, bh.Y)
	}
	mid, w, b := l.Corners(0.5)
	if !near(mid.Y, 0, 1e-9) || !near(mid.X, l.TriR, 1e-9) {
		t.Errorf("flat corner = %+v", mid)
	}
	if !near(w.X, b.X, 1e-9) || w.Y >= b.Y {
		t.Errorf("white %+v / black %+v should share a vertical edge, white on top", w, b)
	}
}

func TestCornersStayInsideTriangleCircle(t *testing.T) {
	l := NewLayout(120, 90, 100, false)
	for n := 0.0; n <= 1.0; n += 0.01 {
		h, w, b := l.Corners(n)
		for _, p := range []Pt{h, w, b} {
			if d := math.Hypot(p.X-l.CX, p.Y-l.CY); !near(d, l.TriR, 1e-9) {
				t.Fatalf("n=%v corner %+v at distance %v, want %v", n, p, d, l.TriR)
			}
		}
	}
}

func TestSVRoundTrip(t *testing.T) {
	l := NewLayout(200, 200, 150, false)
	rng := rand.New(rand.NewSource(3))
	for i := 0; i < 3000; i++ {
		m := Metric(i % 3)
		h := rng.Float64() * 360
		n := NaturalValue(h, m)
		s, v := rng.Float64(), 0.02+rng.Float64()*0.98
		gs, gv := l.PointToSV(n, l.SVToPoint(n, s, v), 0.5)
		if !near(gs, s, 1e-6) || !near(gv, v, 1e-6) {
			t.Fatalf("metric %v hue %v: (%v,%v) -> (%v,%v)", m, h, s, v, gs, gv)
		}
	}
}

func TestBlackCornerKeepsSaturation(t *testing.T) {
	l := NewLayout(200, 200, 150, false)
	_, _, black := l.Corners(0.4)
	s, v := l.PointToSV(0.4, black, 0.37)
	if v != 0 || s != 0.37 {
		t.Errorf("black corner -> s=%v v=%v", s, v)
	}
}

func TestPointToSVClampsOutside(t *testing.T) {
	l := NewLayout(200, 200, 150, false)
	rng := rand.New(rand.NewSource(4))
	for i := 0; i < 2000; i++ {
		p := Pt{rng.Float64()*800 - 300, rng.Float64()*800 - 300}
		s, v := l.PointToSV(rng.Float64(), p, 0.5)
		if s < 0 || s > 1 || v < 0 || v > 1 || math.IsNaN(s) || math.IsNaN(v) {
			t.Fatalf("p=%+v -> s=%v v=%v", p, s, v)
		}
	}
	ph, pw, pb := l.Corners(0.3)
	for i := 0; i < 500; i++ {
		p := Pt{rng.Float64()*800 - 300, rng.Float64()*800 - 300}
		q := ClosestInTriangle(p, ph, pw, pb)
		a, b, c := Barycentric(q, ph, pw, pb)
		if a < -1e-9 || b < -1e-9 || c < -1e-9 {
			t.Fatalf("closest point %+v outside triangle (%v,%v,%v)", q, a, b, c)
		}
		if q2 := ClosestInTriangle(q, ph, pw, pb); math.Hypot(q2.X-q.X, q2.Y-q.Y) > 1e-9 {
			t.Fatalf("closest point is not stable: %+v -> %+v", q, q2)
		}
	}
}

func TestHueGeometry(t *testing.T) {
	for _, ccw := range []bool{false, true} {
		l := NewLayout(100, 100, 80, ccw)
		bottom := l.HueToPoint(240, 50)
		top := l.HueToPoint(60, 50)
		if !near(bottom.X, 100, 1e-9) || !near(bottom.Y, 150, 1e-9) {
			t.Errorf("ccw=%v blue at %+v, want straight down", ccw, bottom)
		}
		if !near(top.X, 100, 1e-9) || !near(top.Y, 50, 1e-9) {
			t.Errorf("ccw=%v yellow at %+v, want straight up", ccw, top)
		}
		for h := 0.0; h < 360; h += 3.7 {
			if got := l.PointToHue(l.HueToPoint(h, 60)); hueDiff(got, h) > 1e-9 {
				t.Fatalf("ccw=%v hue %v -> %v", ccw, h, got)
			}
		}
	}
	cw := NewLayout(0, 0, 100, false).HueToPoint(300, 50) // magenta, just past blue
	ccw := NewLayout(0, 0, 100, true).HueToPoint(300, 50)
	if !(cw.X < 0 && ccw.X > 0) {
		t.Errorf("direction flag broken: cw=%+v ccw=%+v", cw, ccw)
	}
}

func TestHit(t *testing.T) {
	l := NewLayout(200, 200, 100, false)
	n := 0.5
	ph, pw, pb := l.Corners(n)
	centroid := Pt{(ph.X + pw.X + pb.X) / 3, (ph.Y + pw.Y + pb.Y) / 3}
	if got := l.Hit(centroid, n); got != RegionTriangle {
		t.Errorf("centroid -> %v", got)
	}
	if got := l.Hit(Pt{200 + (l.InnerR+l.OuterR)/2, 200}, n); got != RegionRing {
		t.Errorf("ring midpoint -> %v", got)
	}
	if got := l.Hit(Pt{200 + 50, 200 - 40}, n); got != RegionNone {
		t.Errorf("gap point -> %v", got)
	}
	if got := l.Hit(Pt{500, 500}, n); got != RegionNone {
		t.Errorf("far point -> %v", got)
	}
}