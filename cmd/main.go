package main

import (
	"flag"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/aleksgrim/go-wp-cloner/internal/cloner"
	"github.com/aleksgrim/go-wp-cloner/internal/config"
	"github.com/aleksgrim/go-wp-cloner/internal/runner"
	"github.com/aleksgrim/go-wp-cloner/internal/ssh"
)

const version = "0.2.0"

func main() {
	var (
		configPath  = flag.String("config", "config.json", "JSON config file")
		domainsPath = flag.String("domains", "domains.txt", "Domains list file")
		testConn    = flag.Bool("test", false, "Test SSH and tools")
		dryRun      = flag.Bool("dry-run", false, "Show plan without execution")
		showVersion = flag.Bool("version", false, "Version")
		workers     = flag.Int("workers", 0, "Override workers count")
	)
	flag.Usage = printUsage
	flag.Parse()

	if *showVersion {
		fmt.Printf("wp-cloner v%s\n", version)
		os.Exit(0)
	}

	cfg, err := config.Load(*configPath)
	if err != nil {
		die("Config: %v", err)
	}
	if *workers > 0 {
		cfg.Clone.Workers = *workers
	}

	if *testConn {
		fmt.Printf("\n🔌 Testing SSH to %s@%s...\n", cfg.Server.User, cfg.Server.Host)
		client := ssh.NewClient(cfg.Server.Host, cfg.Server.Port, cfg.Server.User, cfg.Server.KeyPath,
			time.Duration(cfg.Clone.CommandTimeoutSec)*time.Second)
		if err := client.Test(); err != nil {
			die("%v", err)
		}
		fmt.Println("\n✅ All set!")
		os.Exit(0)
	}

	domains, err := config.LoadDomains(*domainsPath)
	if err != nil {
		die("Domains: %v", err)
	}

	printHeader(cfg, domains)

	if *dryRun {
		fmt.Printf("\n[DRY RUN] %d domains:\n\n", len(domains))
		fmt.Printf("  %-5s  %-35s  %-28s  %-25s  %s\n", "#", "DOMAIN", "SYSTEM USER", "DB NAME", "WEBROOT")
		fmt.Printf("  %s\n", strings.Repeat("-", 115))
		for i, d := range domains {
			fmt.Printf("  %-5d  %-35s  %-28s  %-25s  %s\n",
				i+1, d,
				cfg.Clone.SiteUser(d),
				config.SiteName(d),
				cfg.Clone.Webroot(d),
			)
		}
		fmt.Printf("\n  PHP-FPM socket: /run/php/%s.sock\n", cfg.Clone.SockName(domains[0]))
		fmt.Printf("  Credentials → %s/\n\n", cfg.Credentials.Dir)
		os.Exit(0)
	}

	startTime := time.Now()
	pool := runner.New(cfg, domains, func(e runner.Event) { printEvent(e) })
	results := pool.Run()
	fmt.Println(runner.Summary(results, time.Since(startTime), cfg.Credentials.Dir))
}

// printHeader displays the tool's branding and the current configuration plan.
func printHeader(cfg *config.Config, domains []string) {
	fmt.Printf(`
╔══════════════════════════════════════════════════════════════════════╗
║             WP Cloner v%s — mass WP cloning               ║
╚══════════════════════════════════════════════════════════════════════╝

  Source:      %s (%s)
  DB:          %s
  Server:      %s:%d
  PHP-FPM:     %s
  Domains:     %d
  Workers:     %d in parallel
  Certbot:     %v
  Credentials: %s/

%s
`,
		version,
		cfg.Source.Domain, cfg.Source.WPPath,
		cfg.Source.DBName,
		cfg.Server.Host, cfg.Server.Port,
		cfg.Clone.PHPVersion,
		len(domains),
		cfg.Clone.Workers,
		cfg.Clone.Certbot,
		cfg.Credentials.Dir,
		strings.Repeat("─", 72),
	)
}

// printEvent handles real-time progress updates from the worker pool.
func printEvent(e runner.Event) {
	domain := pad(e.Domain, 36)
	switch e.Type {
	case runner.EventStep:
		if e.Step == nil {
			return
		}
		switch e.Step.Status {
		case cloner.StatusRunning:
			fmt.Printf("  [%s] ⏳ %s\n", domain, e.Step.Name)
		case cloner.StatusDone:
			fmt.Printf("  [%s] ✓  %-22s %s\n", domain, e.Step.Name, fmtDur(e.Step.Elapsed))
		case cloner.StatusFailed:
			fmt.Printf("  [%s] ✗  %-22s ERROR: %s\n", domain, e.Step.Name, shortStr(e.Step.Error, 80))
		case cloner.StatusSkipped:
			fmt.Printf("  [%s] -  %-22s (skipped)\n", domain, e.Step.Name)
		}
	case runner.EventDone:
		if e.Result == nil {
			return
		}
		if e.Result.Success {
			fmt.Printf("\n  ✅  %s — done in %s [%d/%d]\n\n", e.Domain, fmtDur(e.Result.Elapsed), e.Done, e.Total)
		} else {
			fmt.Printf("\n  ❌  %s — ERROR [%d/%d]\n\n", e.Domain, e.Done, e.Total)
		}
	}
}

// printUsage prints the CLI help message.
func printUsage() {
	fmt.Fprintf(os.Stderr, `wp-cloner v%s

Usage:
  wp-cloner [flags]

Flags:
  -config   string  JSON config (default: config.json)
  -domains  string  Domains list file (default: domains.txt)
  -workers  int     Override workers count
  -test             Test SSH and tools
  -dry-run          Show plan
  -version          Version

`, version)
}

// die prints an error message and exits the program with a non-zero status.
func die(format string, args ...any) {
	fmt.Fprintf(os.Stderr, "❌ "+format+"\n", args...)
	os.Exit(1)
}

// pad truncates or pads a string to a specific length.
func pad(s string, n int) string {
	if len(s) >= n {
		return s[:n-3] + "..."
	}
	return fmt.Sprintf("%-*s", n, s)
}

// fmtDur formats a duration into a human-readable string (e.g., 1.5s or 2m30s).
func fmtDur(d time.Duration) string {
	if d < time.Minute {
		return fmt.Sprintf("%.1fs", d.Seconds())
	}
	m := int(d.Minutes())
	return fmt.Sprintf("%dm%.0fs", m, d.Seconds()-float64(m)*60)
}

// shortStr truncates an error message to a maximum length for cleaner CLI output.
func shortStr(s string, max int) string {
	if len(s) > max {
		return s[:max-3] + "..."
	}
	return s
}
