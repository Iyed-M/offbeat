package main

import (
	"context"
	"flag"
	"fmt"
	"os"

	"github.com/Iyed-M/offbeat/internal/app"
)

func main() {
	var (
		configPath string
		homeDir    string
		showVer    bool
	)
	flag.StringVar(&configPath, "config", "", "path to config.toml (default ~/.config/offbeat/config.toml)")
	flag.StringVar(&homeDir, "home", "", "user home override (defaults to $HOME or $OFFBEAT_HOME)")
	flag.BoolVar(&showVer, "version", false, "print version and exit")
	flag.Parse()

	if showVer {
		fmt.Println("offbeatd", version)
		return
	}

	d, err := app.NewDaemon(context.Background(), app.Options{
		ConfigPath: configPath,
		HomeDir:    homeDir,
		Version:    version,
	})
	if err != nil {
		fmt.Fprintf(os.Stderr, "offbeatd: %v\n", err)
		os.Exit(1)
	}
	defer func() { _ = d.Close() }()

	if err := d.Run(context.Background(), app.RunOptions{}); err != nil {
		d.Logger.Error("daemon exited with error", "err", err)
		os.Exit(1)
	}
}
