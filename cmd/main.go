package main

import (
	"flag"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/aleksgrim/wp-cloner/internal/cloner"
	"github.com/aleksgrim/wp-cloner/internal/config"
	"github.com/aleksgrim/wp-cloner/internal/runner"
	"github.com/aleksgrim/wp-cloner/internal/ssh"
)

const version = "0.2.0"

func main() {
	var (
		configPath  = flag.String("config", "config.json", "Конфиг файл JSON")
		domainsPath = flag.String("domains", "domains.txt", "Файл со списком доменов")
		testConn    = flag.Bool("test", false, "Проверить SSH и инструменты")
		dryRun      = flag.Bool("dry-run", false, "Показать план без выполнения")
		showVersion = flag.Bool("version", false, "Версия")
		workers     = flag.Int("workers", 0, "Переопределить кол-во воркеров")
	)
	flag.Usage = printUsage
	flag.Parse()

	if *showVersion {
		fmt.Printf("wp-cloner v%s\n", version)
		os.Exit(0)
	}

	cfg, err := config.Load(*configPath)
	if err != nil {
		die("Конфиг: %v", err)
	}
	if *workers > 0 {
		cfg.Clone.Workers = *workers
	}

	if *testConn {
		fmt.Printf("\n🔌 Проверяем SSH к %s@%s...\n", cfg.Server.User, cfg.Server.Host)
		client := ssh.NewClient(cfg.Server.Host, cfg.Server.Port, cfg.Server.User, cfg.Server.KeyPath)
		if err := client.Test(); err != nil {
			die("%v", err)
		}
		fmt.Println("\n✅ Всё готово!")
		os.Exit(0)
	}

	domains, err := config.LoadDomains(*domainsPath)
	if err != nil {
		die("Домены: %v", err)
	}

	printHeader(cfg, domains)

	if *dryRun {
		fmt.Printf("\n[DRY RUN] %d доменов:\n\n", len(domains))
		fmt.Printf("  %-5s  %-35s  %-28s  %-25s  %s\n", "#", "ДОМЕН", "СИСТЕМНЫЙ ЮЗЕР", "DB NAME", "WEBROOT")
		fmt.Printf("  %s\n", strings.Repeat("-", 115))
		for i, d := range domains {
			fmt.Printf("  %-5d  %-35s  %-28s  %-25s  %s\n",
				i+1, d,
				cfg.Clone.SiteUser(d),
				config.SiteName(d),
				cfg.Clone.Webroot(d),
			)
		}
		fmt.Printf("\n  PHP-FPM сокет: /run/php/%s.sock\n", cfg.Clone.SockName(domains[0]))
		fmt.Printf("  Credentials → %s/\n\n", cfg.Credentials.Dir)
		os.Exit(0)
	}

	startTime := time.Now()
	pool := runner.New(cfg, domains, func(e runner.Event) { printEvent(e) })
	results := pool.Run()
	fmt.Println(runner.Summary(results, time.Since(startTime), cfg.Credentials.Dir))
}

func printHeader(cfg *config.Config, domains []string) {
	fmt.Printf(`
╔══════════════════════════════════════════════════════════════════════╗
║             WP Cloner v%s — массовое клонирование WP              ║
╚══════════════════════════════════════════════════════════════════════╝

  Источник:    %s (%s)
  БД:          %s
  Сервер:      %s:%d
  PHP-FPM:     %s
  Доменов:     %d
  Воркеров:    %d параллельно
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
			fmt.Printf("  [%s] -  %-22s (пропущен)\n", domain, e.Step.Name)
		}
	case runner.EventDone:
		if e.Result == nil {
			return
		}
		if e.Result.Success {
			fmt.Printf("\n  ✅  %s — готово за %s [%d/%d]\n\n", e.Domain, fmtDur(e.Result.Elapsed), e.Done, e.Total)
		} else {
			fmt.Printf("\n  ❌  %s — ОШИБКА [%d/%d]\n\n", e.Domain, e.Done, e.Total)
		}
	}
}

func printUsage() {
	fmt.Fprintf(os.Stderr, `wp-cloner v%s

Использование:
  wp-cloner [флаги]

Флаги:
  -config   string  JSON конфиг (по умолчанию: config.json)
  -domains  string  Файл доменов (по умолчанию: domains.txt)
  -workers  int     Переопределить кол-во воркеров
  -test             Проверить SSH
  -dry-run          Показать план
  -version          Версия

`, version)
}

func die(format string, args ...any) {
	fmt.Fprintf(os.Stderr, "❌ "+format+"\n", args...)
	os.Exit(1)
}

func pad(s string, n int) string {
	if len(s) >= n {
		return s[:n-3] + "..."
	}
	return fmt.Sprintf("%-*s", n, s)
}

func fmtDur(d time.Duration) string {
	if d < time.Minute {
		return fmt.Sprintf("%.1fs", d.Seconds())
	}
	m := int(d.Minutes())
	return fmt.Sprintf("%dm%.0fs", m, d.Seconds()-float64(m)*60)
}

func shortStr(s string, max int) string {
	if len(s) > max {
		return s[:max-3] + "..."
	}
	return s
}
