// Command desktop-check runs each desktop operation once with harmless inputs and prints the outcome.
package main

import (
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/partprogramming09/winbridge/agent/internal/desktop"
)

var failed int

// step runs one check and prints its outcome and duration.
func step(name string, f func() (string, error)) {
	start := time.Now()
	detail, err := f()
	took := time.Since(start).Round(time.Millisecond)
	if err != nil {
		failed++
		fmt.Printf("[FAIL] %-22s %6s  %v\n", name, took, err)
		return
	}
	fmt.Printf("[ OK ] %-22s %6s  %s\n", name, took, detail)
}

func main() {
	d, err := desktop.New()
	if err != nil {
		fmt.Println("desktop:", err)
		os.Exit(1)
	}
	out := "desktop-check.jpg"
	if len(os.Args) > 1 {
		out = os.Args[1]
	}
	step("Screenshot all", func() (string, error) {
		b, err := d.Screenshot(desktop.ScreenshotOptions{Quality: 75})
		if err == nil {
			err = os.WriteFile(out, b, 0o600)
		}
		return fmt.Sprintf("%d bytes -> %s", len(b), out), err
	})
	step("Screenshot 960 q50", func() (string, error) {
		b, err := d.Screenshot(desktop.ScreenshotOptions{Quality: 50, MaxWidth: 960})
		return fmt.Sprintf("%d bytes", len(b)), err
	})
	step("Screenshot monitor 1", func() (string, error) {
		b, err := d.Screenshot(desktop.ScreenshotOptions{Quality: 75, Monitor: 1})
		return fmt.Sprintf("%d bytes", len(b)), err
	})
	step("Screenshot monitor 9", func() (string, error) {
		_, err := d.Screenshot(desktop.ScreenshotOptions{Quality: 75, Monitor: 9})
		if err == nil {
			return "", fmt.Errorf("a missing monitor must fail")
		}
		return "refused: " + err.Error(), nil
	})
	var windows []desktop.Window
	step("Windows", func() (string, error) {
		var err error
		windows, err = d.Windows()
		var titles []string
		for i, w := range windows {
			if i == 3 {
				break
			}
			titles = append(titles, fmt.Sprintf("%q", w.Title))
		}
		return fmt.Sprintf("%d windows, e.g. %s", len(windows), strings.Join(titles, ", ")), err
	})
	step("Elements", func() (string, error) {
		els, err := d.Elements()
		return fmt.Sprintf("%d controls in the foreground window", len(els)), err
	})
	step("SystemLanguage", func() (string, error) { return d.SystemLanguage(), nil })
	step("SystemInfo", func() (string, error) {
		s, err := d.SystemInfo()
		return "\n" + s, err
	})
	step("MoveTo (same place)", func() (string, error) { return "", d.MoveTo(0, 0, 0) })
	step("Hotkey shift", func() (string, error) { return "", d.Hotkey([]string{"shift"}) })
	step("Hotkey bad key", func() (string, error) {
		if err := d.Hotkey([]string{"nokey"}); err == nil {
			return "", fmt.Errorf("an unknown key must fail")
		}
		return "refused", nil
	})
	step("Focus foreground", func() (string, error) {
		if len(windows) == 0 {
			return "skipped: no windows", nil
		}
		title, err := d.Focus(windows[0].Handle)
		return fmt.Sprintf("%q", title), err
	})
	if failed > 0 {
		fmt.Printf("\n%d check(s) failed\n", failed)
		os.Exit(1)
	}
	fmt.Println("\nall checks passed")
}
