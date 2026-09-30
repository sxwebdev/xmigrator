package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"runtime/debug"
	"syscall"

	"github.com/sxwebdev/xmigrator/cmd/xmigrator/internal/app"
)

var version string

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := app.Run(ctx, os.Args, os.Stdout, os.Stderr, binaryVersion(version, debug.ReadBuildInfo)); err != nil {
		fmt.Fprintln(os.Stderr, "xmigrator:", app.ErrorMessage(err))
		os.Exit(1)
	}
}

func binaryVersion(override string, read func() (*debug.BuildInfo, bool)) string {
	if override != "" {
		return override
	}
	if info, ok := read(); ok && info.Main.Version != "" && info.Main.Version != "(devel)" {
		return info.Main.Version
	}
	return "dev"
}
