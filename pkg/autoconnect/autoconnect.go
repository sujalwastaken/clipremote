// Package autoconnect finds the Clip Studio Paint companion share URL for a
// command-line tool: either the URL passed by the user, or — by screenshotting
// every display and decoding the on-screen "Connect to smartphone" QR code.
//
// It mirrors the behavior of cmd/server (including the LAN-only safety check
// against decoy QR codes) so other binaries can reuse it. On Windows, importing
// this package also makes the process per-monitor DPI-aware before any screen
// capture runs (see dpi_windows.go).
package autoconnect

import (
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/chocolatkey/clipremote"
	"github.com/chocolatkey/clipremote/pkg/qrscan"
	"github.com/sirupsen/logrus"
)

// ShareURL returns the companion share URL to connect with and whether it came
// from a screen scan. If arg is a URL it is returned as is; if arg is empty or
// the literal "scan" the display(s) are screenshotted repeatedly until the QR
// code appears or ctx expires. displays optionally limits the scan to display
// indices.
func ShareURL(ctx context.Context, arg string, displays []int) (string, bool, error) {
	switch arg {
	case "", "scan", "--scan", "-scan":
		// fall through to screen scanning
	default:
		return arg, false, nil
	}

	var wait time.Duration
	if dl, ok := ctx.Deadline(); ok {
		wait = time.Until(dl).Round(time.Second)
	}
	logrus.Infof("no QR URL given — repeatedly screenshotting %s to find the CSP companion QR code "+
		"(keep the \"Connect to smartphone\" dialog visible; waiting up to %s)…", scopeLabel(displays), wait)

	// Wall-clock progress so the wait never looks hung.
	go func() {
		t := time.NewTicker(10 * time.Second)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
				if dl, ok := ctx.Deadline(); ok {
					logrus.Infof("…still scanning for the QR code (%s left)", time.Until(dl).Round(time.Second))
				}
			}
		}
	}()

	url, err := qrscan.Find(ctx, clipremote.IsShareURL, 0, nil, displays...)
	if err != nil {
		if errors.Is(err, context.DeadlineExceeded) {
			return "", true, fmt.Errorf("no CSP companion QR code found on screen within %s "+
				"(is the QR dialog open and fully visible?) — you can also pass the URL explicitly: "+
				"\"https://companion.clip-studio.com/rc/...\"", wait)
		}
		return "", true, err // e.g. qrscan.ErrNoDisplays, already actionable
	}

	// Show where this QR will connect (password redacted) and guard against a
	// decoy QR on screen redirecting the connection off the LAN.
	if ips, port, _, _, derr := clipremote.DecodeConfig(url); derr == nil {
		logrus.Infof("found the CSP companion QR code on screen ✔ — host %s:%d", strings.Join(ips, ","), port)
		if os.Getenv("CLIPREMOTE_ALLOW_PUBLIC") == "" {
			for _, ip := range ips {
				if !IsLANAddr(ip) {
					return "", true, fmt.Errorf("the scanned QR points to non-LAN address %q, which is unexpected for "+
						"CSP companion mode (a decoy QR on screen could redirect this password-bearing connection); "+
						"set CLIPREMOTE_ALLOW_PUBLIC=1 to override, or pass the URL explicitly", ip)
				}
			}
		}
	} else {
		logrus.Infoln("found the CSP companion QR code on screen ✔")
	}
	return url, true, nil
}

// StaleQRHint returns guidance to append to a connect/auth failure when the URL
// was scanned, since a stale on-screen QR is a common cause.
func StaleQRHint(scanned bool) string {
	if !scanned {
		return ""
	}
	return "\n(auto-detect may have read a stale/old QR — re-open the CSP \"Connect to smartphone\" dialog, " +
		"or pass the current URL explicitly: \"https://companion.clip-studio.com/rc/...\")"
}

// IsLANAddr reports whether host is the kind of address a CSP companion host on
// a local network should advertise: private (RFC1918/ULA), loopback, link-local,
// or CGNAT/Tailscale (100.64.0.0/10). A non-literal host (a name) is allowed.
func IsLANAddr(host string) bool {
	ip := net.ParseIP(host)
	if ip == nil {
		return true // hostname, not classifiable here — allow
	}
	if ip.IsLoopback() || ip.IsPrivate() || ip.IsLinkLocalUnicast() {
		return true
	}
	if ip4 := ip.To4(); ip4 != nil && ip4[0] == 100 && ip4[1] >= 64 && ip4[1] <= 127 {
		return true // 100.64.0.0/10 shared address space (CGNAT, Tailscale)
	}
	return false
}

// ParseDisplays parses a display list such as "0" or "0,2" into indices,
// dropping anything that is not a non-negative integer.
func ParseDisplays(s string) []int {
	if strings.TrimSpace(s) == "" {
		return nil
	}
	var out []int
	for _, part := range strings.Split(s, ",") {
		if n, err := strconv.Atoi(strings.TrimSpace(part)); err == nil && n >= 0 {
			out = append(out, n)
		}
	}
	return out
}

func scopeLabel(displays []int) string {
	if len(displays) == 0 {
		return "all displays"
	}
	return fmt.Sprintf("display %v", displays)
}