package remover

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/aleksgrim/go-wp-cloner/internal/config"
	"github.com/aleksgrim/go-wp-cloner/internal/ssh"
)

// StepStatus mirrors cloner.StepStatus so the runner can use a single Event type.
type StepStatus string

const (
	StatusRunning StepStatus = "running"
	StatusDone    StepStatus = "done"
	StatusFailed  StepStatus = "failed"
	StatusSkipped StepStatus = "skipped"
)

// Step is one atomic removal action.
type Step struct {
	Name    string
	Status  StepStatus
	Error   string
	Elapsed time.Duration
}

// Result is the final outcome of removing one domain.
type Result struct {
	Domain  string
	Success bool
	ErrStr  string
	Steps   []Step
	Elapsed time.Duration
}

// OnStepFn is fired after every step status change.
type OnStepFn func(domain string, step Step)

// Remover performs the full teardown of a cloned site.
type Remover struct {
	cfg    *config.Config
	client *ssh.Client
	sysMu  *sync.Mutex
}

// New creates a Remover.
func New(cfg *config.Config, client *ssh.Client, sysMu *sync.Mutex) *Remover {
	return &Remover{cfg: cfg, client: client, sysMu: sysMu}
}

// Remove tears down every resource that was created for the given domain.
// Steps are best-effort — a failure is recorded but execution continues so
// that as much as possible is cleaned up even in a partial state.
func (r *Remover) Remove(domain string, onStep OnStepFn) Result {
	started := time.Now()
	result := Result{Domain: domain}

	cfg := r.cfg.Clone

	siteUser := cfg.SiteUser(domain)
	siteName := config.SiteName(domain)
	webroot := cfg.Webroot(domain)
	chrootDir := cfg.ChrootDir(domain)
	poolName := cfg.PoolName(domain)
	phpVer := cfg.PHPVersion

	type stepDef struct {
		name   string
		serial bool
		run    func() error
	}

	steps := []stepDef{
		{
			// Disable vhost first so the site goes offline immediately.
			name: "Nginx vhost",
			run:  func() error { return r.stepRemoveNginxVhost(domain) },
		},
		{
			name:   "PHP-FPM pool",
			serial: true,
			run:    func() error { return r.stepRemovePHPPool(poolName, phpVer) },
		},
		{
			name: "MySQL DB & user",
			run:  func() error { return r.stepRemoveMySQL(siteName) },
		},
		{
			name: "Files & dirs",
			run:  func() error { return r.stepRemoveDirs(webroot, chrootDir) },
		},
		{
			name:   "System user",
			serial: true,
			run:    func() error { return r.stepRemoveSystemUser(siteUser) },
		},
		{
			name:   "SSH chroot block",
			serial: true,
			run:    func() error { return r.stepRemoveSSHChrootBlock(siteUser) },
		},
		{
			// Local step — no SSH needed.
			name: "Local credentials",
			run:  func() error { return r.stepRemoveLocalCreds(domain) },
		},
	}

	var firstErr error

	for _, s := range steps {
		step := Step{Name: s.name, Status: StatusRunning}
		onStep(domain, step)

		stepStart := time.Now()

		var runErr error
		if s.serial {
			r.sysMu.Lock()
			runErr = s.run()
			r.sysMu.Unlock()
		} else {
			runErr = s.run()
		}

		step.Elapsed = time.Since(stepStart)

		if runErr != nil {
			step.Status = StatusFailed
			step.Error = runErr.Error()
			if firstErr == nil {
				firstErr = fmt.Errorf("[%s] %w", s.name, runErr)
			}
		} else {
			step.Status = StatusDone
		}

		result.Steps = append(result.Steps, step)
		onStep(domain, step)
	}

	result.Elapsed = time.Since(started)
	if firstErr == nil {
		result.Success = true
	} else {
		result.ErrStr = firstErr.Error()
	}
	return result
}

// ── Step implementations ─────────────────────────────────────────────────────

func (r *Remover) stepRemoveNginxVhost(domain string) error {
	var errs []string

	// Remove symlink from sites-enabled (ignore "no such file" via -f).
	if _, err := r.client.RunSudo(fmt.Sprintf(
		"rm -f /etc/nginx/sites-enabled/%s", domain,
	)); err != nil {
		errs = append(errs, fmt.Sprintf("unlink sites-enabled: %v", err))
	}

	// Remove the vhost config itself.
	if _, err := r.client.RunSudo(fmt.Sprintf(
		"rm -f /etc/nginx/sites-available/%s", domain,
	)); err != nil {
		errs = append(errs, fmt.Sprintf("remove sites-available: %v", err))
	}

	// Reload nginx only if config test passes.
	if res, _ := r.client.RunSudo("nginx -t 2>&1"); res == nil || res.ExitCode == 0 {
		r.client.RunSudo("systemctl reload nginx") //nolint:errcheck
	}

	if len(errs) > 0 {
		return fmt.Errorf("%s", strings.Join(errs, "; "))
	}
	return nil
}

func (r *Remover) stepRemovePHPPool(poolName, phpVer string) error {
	confPath := fmt.Sprintf("/etc/php/%s/fpm/pool.d/%s.conf", phpVer, poolName)

	if _, err := r.client.RunSudo(fmt.Sprintf("rm -f %s", confPath)); err != nil {
		return fmt.Errorf("removing pool config: %w", err)
	}
	if _, err := r.client.RunSudo(fmt.Sprintf("systemctl restart php%s-fpm", phpVer)); err != nil {
		return fmt.Errorf("restarting php-fpm: %w", err)
	}
	return nil
}

func (r *Remover) stepRemoveMySQL(siteName string) error {
	rootPass := r.cfg.Clone.DBRootPass
	auth := fmt.Sprintf("sudo mysql -uroot -p'%s'", rootPass)

	cmds := []string{
		fmt.Sprintf(`%s -e "DROP DATABASE IF EXISTS %s;"`, auth, siteName),
		fmt.Sprintf(`%s -e "DROP USER IF EXISTS '%s'@'localhost'; FLUSH PRIVILEGES;"`, auth, siteName),
	}

	var errs []string
	for _, cmd := range cmds {
		if _, err := r.client.RunOrFail(cmd); err != nil {
			errs = append(errs, err.Error())
		}
	}
	if len(errs) > 0 {
		return fmt.Errorf("%s", strings.Join(errs, "; "))
	}
	return nil
}

func (r *Remover) stepRemoveDirs(webroot, chrootDir string) error {
	// Remove webroot first, then the chroot parent.
	cmds := []string{
		fmt.Sprintf("rm -rf %s", webroot),
		fmt.Sprintf("rm -rf %s", chrootDir),
	}
	for _, cmd := range cmds {
		if _, err := r.client.RunSudo(cmd); err != nil {
			return err
		}
	}
	return nil
}

func (r *Remover) stepRemoveSystemUser(siteUser string) error {
	// Check user exists first.
	res, _ := r.client.Run(fmt.Sprintf("id -u %s 2>/dev/null", siteUser))
	if res == nil || res.ExitCode != 0 {
		// User does not exist — nothing to do.
		return nil
	}

	// Kill any lingering processes owned by this user (e.g. hung PHP-FPM workers).
	// pkill returns exit 1 when no processes matched — that's fine, ignore it.
	r.client.RunSudo(fmt.Sprintf("pkill -9 -u %s 2>/dev/null || true", siteUser)) //nolint:errcheck

	if _, err := r.client.RunSudo(fmt.Sprintf("userdel %s", siteUser)); err != nil {
		return fmt.Errorf("userdel: %w", err)
	}
	return nil
}

func (r *Remover) stepRemoveSSHChrootBlock(siteUser string) error {
	marker := fmt.Sprintf("SFTP CHROOT %s", siteUser)

	// Quietly remove the block (sed is idempotent if block is absent).
	if _, err := r.client.RunSudo(fmt.Sprintf(
		`sed -i '/# BEGIN %s/,/# END %s/d' /etc/ssh/sshd_config`,
		marker, marker,
	)); err != nil {
		return fmt.Errorf("removing sshd_config block: %w", err)
	}

	// Validate and reload.
	if _, err := r.client.RunSudo("sshd -t"); err != nil {
		return fmt.Errorf("sshd -t: %w", err)
	}
	if _, err := r.client.RunOrFail("sudo systemctl reload ssh || sudo systemctl reload sshd"); err != nil {
		return fmt.Errorf("reload ssh: %w", err)
	}
	return nil
}

func (r *Remover) stepRemoveLocalCreds(domain string) error {
	dir := filepath.Join(r.cfg.Credentials.Dir, domain)
	if _, err := os.Stat(dir); os.IsNotExist(err) {
		// Already gone — not an error.
		return nil
	}
	if err := os.RemoveAll(dir); err != nil {
		return fmt.Errorf("removing %s: %w", dir, err)
	}
	return nil
}
