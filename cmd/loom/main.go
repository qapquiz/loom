// Command loom attaches to an hc core and renders one terminal on a bloom
// tree. Socket discovery follows hc: HC_SOCKET, HC_TERMINAL_SOCKET, then the
// XDG runtime/state paths.
package main

import (
	"fmt"
	"os"

	tea "charm.land/bubbletea/v2"

	"loom/hc"
	"loom/ui"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "loom:", err)
		os.Exit(1)
	}
}

func run() error {
	sess, err := hc.Dial(hc.DefaultSocket(), hc.Features{AutomaticInput: true})
	if err != nil {
		return fmt.Errorf("dial hc core: %w", err)
	}
	defer sess.Close()

	app := ui.New(sess)
	if _, err := tea.NewProgram(app).Run(); err != nil {
		return fmt.Errorf("run: %w", err)
	}
	return nil
}
