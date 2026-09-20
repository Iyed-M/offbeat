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
	case "spotify":
		if len(rest) != 1 || rest[0] != "sync" {
			fmt.Fprintln(os.Stderr, "offbeat: usage: offbeat spotify sync")
			os.Exit(2)
		}
		os.Exit(runSpotifySync(configPath, homeDir))
	case "missing":
		if len(rest) > 0 {
			fmt.Fprintln(os.Stderr, "offbeat: 'missing' takes no arguments")
			os.Exit(2)
		}
		os.Exit(runMissing(configPath, homeDir))
	case "acquire":
		if len(rest) == 1 && rest[0] == "missing" {
			os.Exit(runAcquireMissing(configPath, homeDir))
		}
		if len(rest) == 1 && rest[0] == "status" {
			os.Exit(runAcquireStatusAll(configPath, homeDir))
		}
		if len(rest) == 2 && rest[0] == "status" {
			os.Exit(runAcquireStatus(configPath, homeDir, rest[1]))
		}
		if len(rest) == 2 && rest[0] == "retry" {
			if rest[1] == "unresolved" {
				os.Exit(runAcquireRetryUnresolved(configPath, homeDir))
			}
			os.Exit(runAcquireRetry(configPath, homeDir, rest[1]))
		}
		if len(rest) == 2 {
			os.Exit(runAcquire(configPath, homeDir, rest[0], rest[1]))
		}
		fmt.Fprintln(os.Stderr, "offbeat: usage: offbeat acquire missing | offbeat acquire <spotify-uri> <authorized-http-url> | offbeat acquire status [id] | offbeat acquire retry <id> | offbeat acquire retry unresolved")
		os.Exit(2)
	default:
		fmt.Fprintf(os.Stderr, "offbeat: unknown command %q\n", cmd)
		fmt.Fprintln(os.Stderr, "run 'offbeat -help' for usage")
		os.Exit(2)
	}
}

func printHelp() {
	fmt.Println("offbeat - Offbeat CLI")
	fmt.Println()
	fmt.Println("Usage: offbeat [flags] <command>")
	fmt.Println()
	fmt.Println("Flags:")
	flag.PrintDefaults()
	fmt.Println()
	fmt.Println("Commands:")
	fmt.Println("  status    report daemon/database status through the control socket")
	fmt.Println("  config        print the daemon's sanitized effective configuration")
	fmt.Println("  spotify sync  request a candidate Spotify snapshot from the adapter")
	fmt.Println("  missing       list supported desired tracks without a managed file")
	fmt.Println("  acquire       request, inspect, or retry authorized media acquisition")
	fmt.Println()
	fmt.Println("More commands arrive in later milestones.")
}
