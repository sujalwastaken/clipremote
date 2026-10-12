package main

import (
	"fmt"
	"math"
	"sync/atomic"
	"time"

	"github.com/hajimehoshi/ebiten/v2"
	"github.com/hajimehoshi/ebiten/v2/inpututil"

	"github.com/chocolatkey/clipremote/pkg/colorwheel"
	"github.com/sirupsen/logrus"
)

// Game is the ebiten application: input handling, color state and drawing.
type Game struct {
	hsv    colorwheel.HSV
	metric colorwheel.Metric
	dir    float64 // +1 hue clockwise, -1 counter-clockwise

	host host
	push *pusher

	rend *renderer
	img  *ebiten.Image

	drag      colorwheel.Region
	lastSent  rgb8
	haveSent  bool
	lastLocal time.Time
	title     string

	pending atomic.Pointer[rgb8] // latest color reported by CSP (network goroutine -> Update)
}

func newGame(metric colorwheel.Metric, ccw bool) *Game {
	dir := 1.0
	if ccw {
		dir = -1
	}
	return &Game{
		hsv:    colorwheel.HSV{H: 30, S: 0.85, V: 0.95},
		metric: metric,
		dir:    dir,
		host:   offlineHost{},
		rend:   &renderer{},
	}
}

// setHost attaches the CSP host and starts the color pusher.
func (g *Game) setHost(h host) {
	g.host = h
	g.push = newPusher(h)
}

// onRemote receives a color change reported by CSP (any goroutine).
func (g *Game) onRemote(c rgb8) { g.pending.Store(&c) }

func (g *Game) rgb() rgb8 {
	r, gg, b := g.hsv.RGB()
	return rgb8{colorwheel.To8(r), colorwheel.To8(gg), colorwheel.To8(b)}
}

// Layout renders at physical-pixel resolution so the wheel is crisp on HiDPI.
func (g *Game) Layout(outW, outH int) (int, int) {
	scale := ebiten.DeviceScaleFactor()
	w := int(math.Round(float64(outW) * scale))
	h := int(math.Round(float64(outH) * scale))
	if w < 1 {
		w = 1
	}
	if h < 1 {
		h = 1
	}
	g.rend.setSize(w, h, scale, g.dir)
	return w, h
}

func (g *Game) Update() error {
	if g.rend.w == 0 {
		return nil
	}
	lay := g.rend.lay

	if inpututil.IsKeyJustPressed(ebiten.KeyM) {
		g.metric = g.metric.Next()
		logrus.Infof("natural value metric: %s", g.metric)
	}
	if inpututil.IsKeyJustPressed(ebiten.KeyT) {
		ebiten.SetWindowFloating(!ebiten.IsWindowFloating())
		logrus.Infof("always on top: %v", ebiten.IsWindowFloating())
	}

	// Pointer: a press picks the region (ring or triangle) that is then dragged
	// until release, even if the pointer leaves it.
	mx, my := ebiten.CursorPosition()
	p := colorwheel.Pt{X: float64(mx), Y: float64(my)}
	n := colorwheel.NaturalValue(g.hsv.H, g.metric)
	if inpututil.IsMouseButtonJustPressed(ebiten.MouseButtonLeft) {
		g.drag = lay.Hit(p, n)
	}
	local := false
	if g.drag != colorwheel.RegionNone {
		if ebiten.IsMouseButtonPressed(ebiten.MouseButtonLeft) {
			switch g.drag {
			case colorwheel.RegionRing:
				g.hsv.H = lay.PointToHue(p)
			case colorwheel.RegionTriangle:
				g.hsv.S, g.hsv.V = lay.PointToSV(n, p, g.hsv.S)
			}
			local = true
		} else {
			g.drag = colorwheel.RegionNone
		}
	}

	// A color reported by CSP moves the wheel, unless the user is mid-gesture
	// (or just was): CSP echoes our own changes back and we must not fight them.
	if c := g.pending.Swap(nil); c != nil &&
		g.drag == colorwheel.RegionNone && time.Since(g.lastLocal) > 400*time.Millisecond {
		if *c != g.rgb() {
			g.hsv = hsvFromRGB8(*c, g.hsv)
			g.lastSent, g.haveSent = *c, true // already CSP's color: don't echo it back
		}
	}

	if local {
		g.lastLocal = time.Now()
		if cur := g.rgb(); !g.haveSent || cur != g.lastSent {
			g.lastSent, g.haveSent = cur, true
			g.push.Push(cur)
		}
	}

	cur := g.rgb()
	title := fmt.Sprintf("CSP Wheel - #%02X%02X%02X - %s - %s", cur.R, cur.G, cur.B, g.metric, g.host.Describe())
	if title != g.title {
		g.title = title
		ebiten.SetWindowTitle(title)
	}
	return nil
}

func (g *Game) Draw(screen *ebiten.Image) {
	pix, changed := g.rend.render(g.hsv, g.metric, g.host.Status())
	if pix == nil {
		return
	}
	w, h := g.rend.w, g.rend.h
	if g.img == nil || g.img.Bounds().Dx() != w || g.img.Bounds().Dy() != h {
		g.img = ebiten.NewImage(w, h)
		changed = true
	}
	if changed {
		g.img.WritePixels(pix)
	}
	screen.DrawImage(g.img, nil)
}

// hsvFromRGB8 converts a color reported by CSP, keeping the wheel's hue for
// grays and its saturation for black (both are undefined in HSV).
func hsvFromRGB8(c rgb8, prev colorwheel.HSV) colorwheel.HSV {
	n := colorwheel.FromRGB(float64(c.R)/255, float64(c.G)/255, float64(c.B)/255)
	if n.S == 0 {
		n.H = prev.H
	}
	if n.V == 0 {
		n.S = prev.S
	}
	return n
}

// ---------------------------------------------------------------------------
// pusher: sends colors to the host off the UI goroutine, latest-wins
// ---------------------------------------------------------------------------

type pusher struct {
	ch chan rgb8
}

func newPusher(h host) *pusher {
	p := &pusher{ch: make(chan rgb8, 1)}
	go func() {
		failing := false
		for c := range p.ch {
			err := h.SetColor(c)
			switch {
			case err != nil && !failing:
				logrus.Warnf("could not set CSP color: %v", err)
				failing = true
			case err == nil && failing:
				logrus.Infoln("CSP color updates working again")
				failing = false
			}
		}
	}()
	return p
}

// Push queues c, replacing any color still waiting to be sent. It must only be
// called from one goroutine (the game loop).
func (p *pusher) Push(c rgb8) {
	for {
		select {
		case p.ch <- c:
			return
		default:
			select {
			case <-p.ch:
			default:
			}
		}
	}
}