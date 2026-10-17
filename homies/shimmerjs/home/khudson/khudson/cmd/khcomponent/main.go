package main

import (
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/shimmerjs/khudson/khudson/component"
	"github.com/shimmerjs/khudson/khudson/internal/axcli"
	"github.com/shimmerjs/khudson/khudson/providers"
)

func run(args []string) error {
	// dock-mirror publishes this executable as its AX action owner.
	if len(args) > 0 && args[0] == "ax" {
		return axcli.Run(args[1:])
	}
	if len(args) == 0 || args[0] == "--list" {
		fmt.Println(strings.Join(providers.Names(), "\n"))
		return nil
	}
	p, err := providers.New(args[0])
	if err != nil {
		return err
	}
	return component.CLI(component.Options{Provider: p, Actions: true, Interval: 5 * time.Second, Timeout: 10 * time.Second,
		Widget: component.Widget{ID: p.Name(), Title: p.Name(), Render: component.Render{Kind: "native", Module: p.Name()}},
	}, args[1:])
}
func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, "khcomponent:", err)
		os.Exit(1)
	}
}
