// Command faehigkeiten installs and tracks agent skills.
package main

import (
	"context"
	"os"
	"os/signal"

	"github.com/Joessst-Dev/faehigkeiten/internal/cli"
)

// Set by GoReleaser via -ldflags.
var (
	version = "dev"
	commit  = "none"
	date    = "unknown"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	code := cli.Execute(ctx, cli.BuildInfo{Version: version, Commit: commit, Date: date}, os.Args[1:], os.Stdout, os.Stderr)
	stop()
	os.Exit(code)
}
