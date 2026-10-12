//go:build windows

package autoconnect

import "syscall"

// init makes the process per-monitor DPI aware before any screen capture runs.
//
// The screenshot library reports each monitor's PHYSICAL pixel bounds, but a
// DPI-unaware process only gets the DWM-virtualized (scaled) desktop when it
// BitBlts — so on a high-DPI/scaled monitor the captured image is clipped and
// stretched, and a small QR code can blur enough to fail decoding. Declaring
// per-monitor-v2 awareness makes the captured pixels match the queried bounds.
//
// Same logic as cmd/server/dpi_windows.go; repeating it here means every binary
// that imports autoconnect is covered. Calling it twice in one process is
// harmless (the second call simply fails).
func init() {
	user32 := syscall.NewLazyDLL("user32.dll")

	// Preferred: SetProcessDpiAwarenessContext(PER_MONITOR_AWARE_V2) == (HANDLE)-4.
	if p := user32.NewProc("SetProcessDpiAwarenessContext"); p.Find() == nil {
		if r, _, _ := p.Call(^uintptr(3)); r != 0 {
			return
		}
	}

	// Fallback: shcore!SetProcessDpiAwareness(PROCESS_PER_MONITOR_DPI_AWARE = 2).
	if p := syscall.NewLazyDLL("shcore.dll").NewProc("SetProcessDpiAwareness"); p.Find() == nil {
		if r, _, _ := p.Call(uintptr(2)); r == 0 {
			return
		}
	}

	// Last resort: user32!SetProcessDPIAware (system-DPI aware; Vista+).
	if p := user32.NewProc("SetProcessDPIAware"); p.Find() == nil {
		_, _, _ = p.Call()
	}
}