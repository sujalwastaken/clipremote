// Command wheel is a small always-on-top color wheel for Clip Studio Paint's
// companion mode: a hue ring with an inscribed triangle whose saturated-hue
// corner slides up and down with the natural value (lightness) of the hue.
// Picking a color sets CSP's current drawing color over the companion protocol.
//
// Usage:
//
//	wheel                  attach to a running cmd/server bridge if there is one
//	                       (re-using its CSP session), otherwise auto-detect the
//	                       CSP "Connect to smartphone" QR on screen and connect
//	wheel scan             force the QR screen scan (direct connection)
//	wheel <QR share URL>   direct connection with an explicit URL
//	wheel -bridge http://localhost:8089
//	wheel -offline         UI only, no CSP
//
// Keys: M cycles the natural-value metric (luma / lab / flat), T toggles
// always-on-top.
package main

import (
	"flag"
	"fmt"
	"os"

	"github.com/hajimehoshi/ebiten/v2"
	"github.com/sirupsen/logrus"

	"github.com/chocolatkey/clipremote/pkg/autoconnect"
	"github.com/chocolatkey/clipremote/pkg/colorwheel"
)

func main() {
	var (
		metricFlag = flag.String("metric", "luma", "natural value of a hue: luma | lab | flat")
		topFlag    = flag.Bool("top", true, "keep the window always on top")
		sizeFlag   = flag.Int("size", 340, "initial window width in device-independent pixels")
		bridgeFlag = flag.String("bridge", "", "attach to a running cmd/server bridge at this URL (e.g. "+defaultBridge+")")
		offline    = flag.Bool("offline", false, "run without a CSP connection (UI test)")
		ccw        = flag.Bool("ccw", false, "hue increases counter-clockwise (default clockwise, blue at the bottom)")
		trace      = flag.Bool("trace", false, "log color pushes from CSP (to learn what the host reports)")
		debug      = flag.Bool("debug", false, "log every protocol frame (password redacted)")
	)
	flag.Usage = func() {
		fmt.Fprintf(flag.CommandLine.Output(), "usage: wheel [flags] [scan | <QR share URL>]\n\nflags:\n")
		flag.PrintDefaults()
	}
	flag.Parse()

	if *debug || os.Getenv("CLIPREMOTE_DEBUG") != "" {
		logrus.SetLevel(logrus.DebugLevel)
	}
	metric, err := colorwheel.ParseMetric(*metricFlag)
	if err != nil {
		logrus.Fatalln(err)
	}

	game := newGame(metric, *ccw)

	// Connect BEFORE opening the window: an always-on-top window could otherwise
	// cover the very QR code the auto-detect is trying to read.
	h, err := connect(game, flag.Arg(0), *bridgeFlag, *offline, *trace)
	if err != nil {
		logrus.Fatalln(err)
	}
	game.setHost(h)
	defer h.Close()
	logrus.Infof("wheel ready (%s, metric %s)", h.Describe(), metric)

	footer := int(footerLogic)
	ebiten.SetWindowTitle("CSP Wheel")
	ebiten.SetWindowSize(*sizeFlag, *sizeFlag+footer)
	ebiten.SetWindowSizeLimits(200, 200+footer, -1, -1)
	ebiten.SetWindowResizingMode(ebiten.WindowResizingModeEnabled)
	ebiten.SetWindowFloating(*topFlag)
	ebiten.SetRunnableOnUnfocused(true) // keep syncing while CSP has the focus
	if err := ebiten.RunGame(game); err != nil {
		logrus.Fatalln(err)
	}
}

// connect picks the CSP transport: an explicit bridge, an explicit/scanned
// direct connection, or (no arguments) a running bridge if one answers on the
// default address, else a screen-scanned direct connection.
func connect(g *Game, arg, bridge string, offline, trace bool) (host, error) {
	switch {
	case offline:
		return offlineHost{}, nil
	case bridge != "":
		if !probeBridge(bridge) {
			return nil, fmt.Errorf("no cmd/server bridge answering at %s (start it with: go run ./cmd/server)", bridge)
		}
		return newBridgeHost(bridge, g.onRemote, trace), nil
	case arg == "" && probeBridge(defaultBridge):
		logrus.Infof("found a running cmd/server bridge at %s — reusing its CSP connection", defaultBridge)
		return newBridgeHost(defaultBridge, g.onRemote, trace), nil
	}
	return dialDirect(arg, autoconnect.ParseDisplays(os.Getenv("CLIPREMOTE_SCAN_DISPLAY")), g.onRemote, trace)
}