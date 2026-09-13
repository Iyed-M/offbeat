package main

import (
	"flag"
	"fmt"
	"os"
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
		if len(rest) > 0 {
			fmt.Fprintf(os.Stderr, "offbeat: 'status' takes no arguments (got %v)\n", rest)
			fmt.Fprintln(os.Stderr, "run 'offbeat -help' for usage")
			os.Exit(2)
		}
		os.Exit(runStatus(configPath, homeDir))
	case "config":
		if len(rest) > 0 {
			fmt.Fprintf(os.Stderr, "offbeat: 'config' takes no arguments (got %v)\n", rest)
			fmt.Fprintln(os.Stderr, "run 'offbeat -help' for usage")
			os.Exit(2)
		}
		os.Exit(runConfig(configPath, homeDir))
	default:
		fmt.Fprintf(os.Stderr, "offbeat: unknown command %q\n", cmd)
		fmt.Fprintln(os.Stderr, "run 'offbeat -help' for usage")
		os.Exit(2)
	}
}

func printHelp() {
	fmt.Println("offbeat - Offbeat CLI (M1)")
	fmt.Println()
	fmt.Println("Usage: offbeat [flags] <command>")
	fmt.Println()
	fmt.Println("Flags:")
	flag.PrintDefaults()
	fmt.Println()
	fmt.Println("Commands:")
	fmt.Println("  status    report daemon/database status through the control socket")
	fmt.Println("  config    print the daemon's sanitized effective configuration")
	fmt.Println()
	fmt.Println("More commands arrive in later milestones.")
}
