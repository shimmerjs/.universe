package axcli

import (
	"errors"
	"flag"
	"fmt"

	"github.com/shimmerjs/khudson/khudson/internal/ax"
)

func Run(args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("ax: need a verb: unminimize <title> [--app <name>] | quit --bundle <id> | force-quit --bundle <id> | status")
	}
	switch args[0] {
	case "status":
		if ax.Trusted() {
			fmt.Println("trusted: yes")
		} else {
			fmt.Println("trusted: no")
		}
		return nil
	case "unminimize":
		rest := args[1:]
		if len(rest) == 0 {
			return fmt.Errorf("ax unminimize: need a window title")
		}
		title := rest[0]
		fs := flag.NewFlagSet("ax unminimize", flag.ContinueOnError)
		app := fs.String("app", "", "owning app for the in-app window fallback")
		if err := fs.Parse(rest[1:]); err != nil {
			return err
		}
		if !ax.Trusted() {
			return fmt.Errorf("ax unminimize: Accessibility not granted -- grant this executable in System Settings > Privacy & Security > Accessibility")
		}
		err := ax.PressMinimizedItem(title)
		if errors.Is(err, ax.ErrNotFound) && *app != "" {
			// tap-vs-sweep race: the dock item vanished between render and
			// tap; fall back to the app's own window attributes
			return ax.UnminimizeWindow(*app, title)
		}
		return err
	case "quit", "force-quit":
		// no AX grant needed: resolution is lsappinfo, the quit an
		// AppleEvent, the kill a plain signal -- resolve + re-validate
		// happen inside the verb, at exec time
		fs := flag.NewFlagSet("ax "+args[0], flag.ContinueOnError)
		bundle := fs.String("bundle", "", "bundle id of the running app")
		if err := fs.Parse(args[1:]); err != nil {
			return err
		}
		if *bundle == "" {
			return fmt.Errorf("ax %s: need --bundle <id>", args[0])
		}
		if args[0] == "quit" {
			return ax.QuitBundle(*bundle)
		}
		return ax.ForceQuitBundle(*bundle)
	default:
		return fmt.Errorf("ax: unknown verb %q (unminimize | quit | force-quit | status)", args[0])
	}
}
