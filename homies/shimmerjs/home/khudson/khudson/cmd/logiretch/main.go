package main

import (
	"fmt"
	"os"
	"time"

	"github.com/shimmerjs/khudson/khudson/component"
	"github.com/shimmerjs/khudson/khudson/internal/module/logiretch"
)

func main() {
	err := component.CLI(component.Options{
		Provider: logiretch.New(), Interval: 100 * time.Millisecond,
		Widget: component.Widget{ID: "logiretch", Title: "Logitech", Chrome: true, Render: component.Render{Kind: "native", Module: "logiretch"}},
	}, os.Args[1:])
	if err != nil {
		fmt.Fprintln(os.Stderr, "logiretch:", err)
		os.Exit(1)
	}
}
