package runner

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/aleksgrim/go-wp-cloner/internal/cloner"
	"github.com/aleksgrim/go-wp-cloner/internal/config"
	"github.com/aleksgrim/go-wp-cloner/internal/ssh"
)

// EventType represents the category of an execution event.
type EventType string

const (
	// EventStep is fired multiple times during a domain's cloning process.
	EventStep EventType = "step"
	// EventDone is fired once when a domain's cloning process is finished.
	EventDone EventType = "done"
)

// Event contains details about the progress of cloning tasks.
type Event struct {
	Type   EventType
	Domain string
	Step   *cloner.Step
	Result *cloner.Result
	Done   int
	Total  int
}

// Pool manages parallel execution of cloning tasks for multiple domains.
type Pool struct {
	cfg     *config.Config
	domains []string
	onEvent func(Event)
	sysMu   *sync.Mutex // global mutex for all workers
}

// New initializes a new Pool with the given configuration and domain list.
func New(cfg *config.Config, domains []string, onEvent func(Event)) *Pool {
	return &Pool{
		cfg:     cfg,
		domains: domains,
		onEvent: onEvent,
		sysMu:   &sync.Mutex{},
	}
}

// Run starts the worker pool and executes cloning tasks in parallel, respecting the worker limit.
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
					fmt.Fprintf(os.Stderr, "⚠️  credentials for %s: %v\n", domain, err)
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

// Summary generates a formatted string showing the overall results and timings of all tasks.
func Summary(results []cloner.Result, totalElapsed time.Duration, credsDir string) string {
	var sb strings.Builder
	var success, failed int

	sb.WriteString("\n" + strings.Repeat("─", 72) + "\n")
	sb.WriteString("  SUMMARY\n")
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
		"  Success: %d  |  Errors: %d  |  Total: %d  |  Time: %s\n",
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
