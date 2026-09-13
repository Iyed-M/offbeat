package main

import (
	"flag"
	"fmt"
	"os"

	"github.com/Iyed-M/offbeat/internal/config"
)

func main() {
	var (
		configPath string
		homeDir    string
		showVer    bool
		showHelp   bool
	)
	flag.StringVar(&configPath, "config", "", "path to config.toml")
	flag.StringVar(&homeDir, "home", "", "user home override")
	flag.BoolVar(&showVer, "version", false, "print version and exit")
	flag.BoolVar(&showHelp, "help", false, "print help and exit")
	flag.Parse()

	if showVer {
		fmt.Println("offbeat", version)
		return
	}

	args := flag.Args()
	if showHelp || len(args) == 0 {
		printHelp()
		if !showHelp {
			os.Exit(1)
		}
		return
	}

	cmd, rest := args[0], args[1:]
	switch cmd {
	case "status":
		os.Exit(runStatus(configPath, homeDir))
	case "config":
		os.Exit(runConfig(configPath, homeDir))
	default:
		fmt.Fprintf(os.Stderr, "offbeat: unknown command %q\n", cmd)
		fmt.Fprintln(os.Stderr, "run 'offbeat -help' for usage")
		os.Exit(2)
	}
	_ = rest
}

func printHelp() {
	fmt.Println("offbeat - Offbeat CLI (M0 skeleton)")
	fmt.Println()
	fmt.Println("Usage: offbeat [flags] <command>")
	fmt.Println()
	fmt.Println("Flags:")
	flag.PrintDefaults()
	fmt.Println()
	fmt.Println("Commands:")
	fmt.Println("  status    report daemon/database status (M0: errors if daemon unreachable)")
	fmt.Println("  config    print the resolved config")
	fmt.Println()
	fmt.Println("More commands arrive in later milestones.")
}

func runStatus(configPath, homeDir string) int {
	fmt.Fprintln(os.Stderr, "offbeat status: not yet connected to a running daemon (M1 will add IPC)")
	cfg, err := loadConfig(configPath, homeDir)
	if err != nil {
		fmt.Fprintf(os.Stderr, "config: %v\n", err)
		return 1
	}
	fmt.Printf("config dir : %s\n", cfg.Paths.ConfigDir)
	fmt.Printf("data dir   : %s\n", cfg.Paths.DataDir)
	fmt.Printf("database   : %s\n", cfg.Paths.Database)
	return 0
}

func runConfig(configPath, homeDir string) int {
	cfg, err := loadConfig(configPath, homeDir)
	if err != nil {
		fmt.Fprintf(os.Stderr, "config: %v\n", err)
		return 1
	}
	fmt.Printf("# resolved from: %s\n", configPath)
	fmt.Printf("config_dir = %q\n", cfg.Paths.ConfigDir)
	fmt.Printf("data_dir   = %q\n", cfg.Paths.DataDir)
	fmt.Printf("database   = %q\n", cfg.Paths.Database)
	fmt.Printf("music_root = %q\n", cfg.Paths.MusicRoot)
	fmt.Printf("socket_dir = %q\n", cfg.Paths.SocketDir)
	fmt.Printf("certs_dir  = %q\n", cfg.Paths.CertsDir)
	fmt.Printf("log_file   = %q\n", cfg.Paths.LogFile)
	fmt.Printf("acquisition.concurrency = %d\n", cfg.Acquisition.Concurrency)
	fmt.Printf("downloader.yt_dlp_path  = %q\n", cfg.Downloader.YTDLPPath)
	fmt.Printf("downloader.ffmpeg_path = %q\n", cfg.Downloader.FFmpegPath)
	return 0
}

func loadConfig(configPath, homeDir string) (config.Config, error) {
	loader := config.NewLoader(homeDir, configPath)
	cfg, err := loader.Load()
	if err != nil {
		def := configPath
		if def == "" {
			def, _ = config.DefaultConfigPath()
		}
		if def != "" {
			return cfg, fmt.Errorf("%w (using config %s)", err, def)
		}
		return cfg, err
	}
	return cfg, nil
}
