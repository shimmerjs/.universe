package main

import (
	"fmt"
	"os"
	"time"

	"github.com/shimmerjs/khudson/khudson/component"
	"github.com/shimmerjs/khudson/khudson/internal/module/cpumem"
	"github.com/shimmerjs/khudson/khudson/internal/module/disk"
	"github.com/shimmerjs/khudson/khudson/internal/module/procs"
	"github.com/shimmerjs/khudson/khudson/internal/module/resources"
)

func main() {
	err := component.CLI(component.Options{
		Provider: resources.New(cpumem.New(), disk.New(), procs.New()), Interval: 5 * time.Second,
		Widget: component.Widget{ID: "resources", Title: "Resources", Render: component.Render{Kind: "native", Module: "resources", Params: map[string]any{"volumes": []any{"/"}}}},
	}, os.Args[1:])
	if err != nil {
		fmt.Fprintln(os.Stderr, "khresources:", err)
		os.Exit(1)
	}
}
