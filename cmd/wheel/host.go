package main

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/url"
	"strings"
	"sync/atomic"
	"time"

	"github.com/chocolatkey/clipremote"
	"github.com/chocolatkey/clipremote/pkg/autoconnect"
	"github.com/chocolatkey/clipremote/pkg/commands"
	"github.com/chocolatkey/clipremote/pkg/packets"
	"github.com/sirupsen/logrus"
)

// rgb8 is an 8-bit RGB color.
type rgb8 struct{ R, G, B uint8 }

// remoteFn is called (from a network goroutine) when CSP reports a color change.
type remoteFn func(rgb8)

// host is where picked colors go: a direct CSP companion connection, a running
// cmd/server bridge, or nothing (UI-only).
type host interface {
	Describe() string
	Status() status
	SetColor(rgb8) error // blocking; called from the pusher goroutine only
	Close()
}

const (
	scanTimeout   = 90 * time.Second
	defaultBridge = "http://127.0.0.1:8089"
)

// ---------------------------------------------------------------------------
// Offline (UI only)
// ---------------------------------------------------------------------------

type offlineHost struct{}

func (offlineHost) Describe() string    { return "offline" }
func (offlineHost) Status() status      { return statusOffline }
func (offlineHost) SetColor(rgb8) error { return nil }
func (offlineHost) Close()              {}

// ---------------------------------------------------------------------------
// Direct: own companion-mode connection through the clipremote library
// ---------------------------------------------------------------------------

type directHost struct {
	c    *clipremote.Client
	addr string
}

// dialDirect resolves the share URL (argument or QR scan), connects and
// authenticates. Host pushes are registered before authenticating so none are
// missed.
func dialDirect(arg string, displays []int, remote remoteFn, trace bool) (host, error) {
	ctx, cancel := context.WithTimeout(context.Background(), scanTimeout)
	defer cancel()
	shareURL, scanned, err := autoconnect.ShareURL(ctx, arg, displays)
	if err != nil {
		return nil, err
	}

	client, password, err := clipremote.ConnectFromURL(shareURL)
	if err != nil {
		return nil, fmt.Errorf("connect failed: %w%s", err, autoconnect.StaleQRHint(scanned))
	}

	// Current color reported by CSP (Color Mix / Sub View tabs carry plain RGB).
	for _, cmd := range []commands.Command{commands.SyncColorMixUIState, commands.SyncSubViewUIState} {
		client.OnServerCommand(cmd, func(pkt *packets.ServerCommand) {
			if trace {
				logrus.Infof("push %s: %s", pkt.Command, pkt.RawDetail)
			}
			if c, ok := rgbFromPush(pkt.RawDetail); ok {
				remote(c)
			}
		})
	}
	if trace { // HSV state of the Color Circle tab: scale unverified, so log only
		client.OnServerCommand(commands.SyncColorCircleUIState, func(pkt *packets.ServerCommand) {
			logrus.Infof("push %s: %s", pkt.Command, pkt.RawDetail)
		})
	}

	res, err := client.Authenticate(password)
	if err != nil {
		_ = client.Close()
		return nil, fmt.Errorf("authentication failed: %w%s", err, autoconnect.StaleQRHint(scanned))
	}
	logrus.Infof("authenticated (host spec %s, quick-access=%v)", res.ServerSpecVersion, res.QuickAccessAvailable)
	return &directHost{c: client, addr: client.RemoteAddr()}, nil
}

func (h *directHost) Describe() string { return "direct " + h.addr }

func (h *directHost) Status() status {
	if h.c.Alive() {
		return statusUp
	}
	return statusDown
}

func (h *directHost) SetColor(c rgb8) error {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	return h.c.SetCurrentColorRGB(ctx, c.R, c.G, c.B, false)
}

func (h *directHost) Close() { _ = h.c.Close() }

// ---------------------------------------------------------------------------
// Bridge: attach to an already-running cmd/server (its CSP session is reused)
// ---------------------------------------------------------------------------

type bridgeHost struct {
	base   string
	hc     *http.Client // short timeouts for commands/status
	sc     *http.Client // no timeout, for the SSE stream
	remote remoteFn
	trace  bool

	up     atomic.Bool
	ctx    context.Context
	cancel context.CancelFunc
}

// probeBridge reports whether a cmd/server bridge answers at base.
func probeBridge(base string) bool {
	hc := &http.Client{Timeout: 700 * time.Millisecond}
	resp, err := hc.Get(strings.TrimRight(base, "/") + "/api/status")
	if err != nil {
		return false
	}
	defer resp.Body.Close()
	return resp.StatusCode == http.StatusOK
}

func newBridgeHost(base string, remote remoteFn, trace bool) *bridgeHost {
	ctx, cancel := context.WithCancel(context.Background())
	b := &bridgeHost{
		base:   strings.TrimRight(base, "/"),
		hc:     &http.Client{Timeout: 3 * time.Second},
		sc:     &http.Client{},
		remote: remote,
		trace:  trace,
		ctx:    ctx,
		cancel: cancel,
	}
	b.pollStatus()
	go func() {
		t := time.NewTicker(2 * time.Second)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
				b.pollStatus()
			}
		}
	}()
	go b.eventLoop()
	return b
}

func (b *bridgeHost) Describe() string { return "bridge " + b.base }

func (b *bridgeHost) Status() status {
	if b.up.Load() {
		return statusUp
	}
	return statusDown
}

func (b *bridgeHost) Close() { b.cancel() }

func (b *bridgeHost) pollStatus() {
	resp, err := b.hc.Get(b.base + "/api/status")
	if err != nil {
		b.up.Store(false)
		return
	}
	defer resp.Body.Close()
	var s struct {
		Alive bool `json:"alive"`
	}
	if resp.StatusCode != http.StatusOK || json.NewDecoder(resp.Body).Decode(&s) != nil {
		b.up.Store(false)
		return
	}
	b.up.Store(s.Alive)
}

func (b *bridgeHost) SetColor(c rgb8) error {
	detail, err := json.Marshal(commands.SetColorRGBRequest{
		RGBColorR: int(c.R), RGBColorG: int(c.G), RGBColorB: int(c.B),
	})
	if err != nil {
		return err
	}
	form := url.Values{}
	form.Set("command", string(commands.SetCurrentColor))
	form.Set("detail", string(detail))
	resp, err := b.hc.PostForm(b.base+"/request", form)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<16))
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("bridge: %s: %s", resp.Status, strings.TrimSpace(string(body)))
	}
	return nil
}

// eventLoop follows the bridge's Server-Sent Events stream (/api/events),
// reconnecting until the host is closed.
func (b *bridgeHost) eventLoop() {
	for b.ctx.Err() == nil {
		if err := b.readEvents(); err != nil && b.ctx.Err() == nil {
			logrus.Debugln("bridge event stream:", err)
		}
		select {
		case <-b.ctx.Done():
			return
		case <-time.After(2 * time.Second):
		}
	}
}

func (b *bridgeHost) readEvents() error {
	req, err := http.NewRequestWithContext(b.ctx, http.MethodGet, b.base+"/api/events", nil)
	if err != nil {
		return err
	}
	resp, err := b.sc.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("events: %s", resp.Status)
	}

	sc := bufio.NewScanner(resp.Body)
	sc.Buffer(make([]byte, 0, 64<<10), 8<<20) // preview pushes can be large
	for sc.Scan() {
		line := sc.Text()
		if !strings.HasPrefix(line, "data: ") {
			continue
		}
		var ev struct {
			Event string          `json:"event"`
			Data  json.RawMessage `json:"data"`
		}
		if json.Unmarshal([]byte(line[len("data: "):]), &ev) != nil {
			continue
		}
		switch ev.Event {
		case string(commands.SyncColorMixUIState), string(commands.SyncSubViewUIState):
			if b.trace {
				logrus.Infof("push %s: %s", ev.Event, ev.Data)
			}
			if c, ok := rgbFromPush(ev.Data); ok {
				b.remote(c)
			}
		case string(commands.SyncColorCircleUIState):
			if b.trace {
				logrus.Infof("push %s: %s", ev.Event, ev.Data)
			}
		}
	}
	return sc.Err()
}

// ---------------------------------------------------------------------------
// Push decoding
// ---------------------------------------------------------------------------

// rgbFromPush extracts the current color from a SyncColorMix/SubView body.
//
// Only a conservative reading is accepted: bodies carrying a tab-only fast path
// (ServerSelectedTabKind set) or a transparent color are ignored, and every
// channel must be within 0..255. Channels outside that range (e.g. if CSP
// reports 32-bit values on the Color Mix tab) are rejected rather than guessed;
// run with -trace to see the raw values.
func rgbFromPush(raw []byte) (rgb8, bool) {
	var s struct {
		Tab         string  `json:"ServerSelectedTabKind"`
		Red         float64 `json:"CurrentColorRed"`
		Green       float64 `json:"CurrentColorGreen"`
		Blue        float64 `json:"CurrentColorBlue"`
		Transparent bool    `json:"IsCurrentColorTransparent"`
	}
	if len(raw) == 0 || json.Unmarshal(raw, &s) != nil {
		return rgb8{}, false
	}
	if s.Tab != "" || s.Transparent {
		return rgb8{}, false
	}
	for _, v := range []float64{s.Red, s.Green, s.Blue} {
		if v < 0 || v > 255 || math.IsNaN(v) {
			return rgb8{}, false
		}
	}
	return rgb8{uint8(math.Round(s.Red)), uint8(math.Round(s.Green)), uint8(math.Round(s.Blue))}, true
}