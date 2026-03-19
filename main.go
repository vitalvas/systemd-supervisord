package main

import (
	"context"
	"os"

	"github.com/vitalvas/systemd-supervisord/internal/cli"
)

var version = "dev"

func main() {
	cmd := cli.NewRootCommand()
	cmd.Version = version

	if err := cmd.ExecuteContext(context.Background()); err != nil {
		os.Exit(1)
	}
}
