package runner

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/aleksgrim/wp-cloner/internal/cloner"
	"github.com/aleksgrim/wp-cloner/internal/config"
	"github.com/aleksgrim/wp-cloner/internal/ssh"
)

type EventType string

const (
	EventStep EventType = "step"
	EventDone EventType = "done"
)

type Event struct {
	Type   EventType
	Domain string
	Step   *cloner.Step
	Result *cloner.Result
	Done   int
	Total  int
}

type Pool struct {
	cfg     *config.Config
	domains []string
	onEvent func(Event)
	sysMu   *sync.Mutex // общий мьютекс для всех воркеров
}

func New(cfg *config.Config, domains []string, onEvent func(Event)) *Pool {
	return &Pool{
		cfg:     cfg,
		domains: domains,
		onEvent: onEvent,
		sysMu:   &sync.Mutex{},
	}
}

func (p *Pool) Run() []cloner.Result {
	total := len(p.domains)
	results := make([]cloner.Result, total)

	var (
		done atomic.Int32
		mu   sync.Mutex
	)

	sem := make(chan struct{}, p.cfg.Clone.Workers)
	var wg sync.WaitGroup

	for i, domain := range p.domains {
		i, domain := i, domain
		wg.Add(1)

		go func() {
			defer wg.Done()

			sem <- struct{}{}
			defer func() { <-sem }()

			client := ssh.NewClient(
				p.cfg.Server.Host,
				p.cfg.Server.Port,
				p.cfg.Server.User,
				p.cfg.Server.KeyPath,
			)
			defer client.Close()

			c := cloner.New(p.cfg, client, p.sysMu)
			result := c.Clone(domain, func(d string, step cloner.Step) {
				p.onEvent(Event{
					Type:   EventStep,
					Domain: d,
					Step:   &step,
					Done:   int(done.Load()),
					Total:  total,
				})
			})

			if result.Success && result.Credentials != nil {
				if err := saveCredentials(p.cfg.Credentials.Dir, result.Credentials); err != nil {
					fmt.Fprintf(os.Stderr, "⚠️  credentials для %s: %v\n", domain, err)
				}
			}

			n := int(done.Add(1))

			mu.Lock()
			results[i] = result
			mu.Unlock()

			p.onEvent(Event{
				Type:   EventDone,
				Domain: domain,
				Result: &result,
				Done:   n,
				Total:  total,
			})
		}()
	}

	wg.Wait()
	return results
}

func saveCredentials(baseDir string, creds *cloner.Credentials) error {
	dir := filepath.Join(baseDir, creds.Domain)
	if err := os.MkdirAll(dir, 0700); err != nil {
		return err
	}

	content := fmt.Sprintf(`=== %s ===
Created: %s

SFTP:
  User:     %s
  Password: %s

MySQL:
  DB:       %s
  User:     %s
  Password: %s

WordPress:
  URL:      http://%s
  Admin:    http://%s/wp-admin
`,
		creds.Domain,
		time.Now().Format("2006-01-02 15:04:05"),
		creds.SiteUser, creds.SFTPPassword,
		creds.DBName, creds.DBUser, creds.DBPassword,
		creds.Domain, creds.Domain,
	)

	return os.WriteFile(filepath.Join(dir, "credentials.txt"), []byte(content), 0600)
}

func Summary(results []cloner.Result, totalElapsed time.Duration, credsDir string) string {
	var sb strings.Builder
	var success, failed int

	sb.WriteString("\n" + strings.Repeat("─", 72) + "\n")
	sb.WriteString("  ИТОГ\n")
	sb.WriteString(strings.Repeat("─", 72) + "\n")

	for _, r := range results {
		if r.Success {
			success++
			sb.WriteString(fmt.Sprintf("  ✅  %-42s %s\n", r.Domain, fmtDur(r.Elapsed)))
		} else {
			failed++
			errMsg := r.ErrStr
			if len(errMsg) > 55 {
				errMsg = errMsg[:52] + "..."
			}
			sb.WriteString(fmt.Sprintf("  ❌  %-42s %s\n", r.Domain, errMsg))
		}
	}

	sb.WriteString(strings.Repeat("─", 72) + "\n")
	sb.WriteString(fmt.Sprintf(
		"  Успешно: %d  |  Ошибки: %d  |  Всего: %d  |  Время: %s\n",
		success, failed, len(results), fmtDur(totalElapsed),
	))
	if success > 0 {
		sb.WriteString(fmt.Sprintf("  Credentials: %s/\n", credsDir))
	}
	sb.WriteString("\n")

	return sb.String()
}

func fmtDur(d time.Duration) string {
	if d < time.Minute {
		return fmt.Sprintf("%.1fs", d.Seconds())
	}
	m := int(d.Minutes())
	return fmt.Sprintf("%dm%.0fs", m, d.Seconds()-float64(m)*60)
}
