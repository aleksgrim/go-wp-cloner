package cloner

import (
	"crypto/rand"
	"encoding/base64"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/aleksgrim/go-wp-cloner/internal/config"
	"github.com/aleksgrim/go-wp-cloner/internal/ssh"
)

type StepStatus string

const (
	StatusPending StepStatus = "pending"
	StatusRunning StepStatus = "running"
	StatusDone    StepStatus = "done"
	StatusFailed  StepStatus = "failed"
	StatusSkipped StepStatus = "skipped"
)

type Step struct {
	Name    string        `json:"name"`
	Status  StepStatus    `json:"status"`
	Error   string        `json:"error,omitempty"`
	Elapsed time.Duration `json:"elapsed"`
}

type Credentials struct {
	Domain       string
	SiteUser     string
	Webroot      string
	DBName       string
	DBUser       string
	DBPassword   string
	SFTPPassword string
}

type Result struct {
	Domain      string        `json:"domain"`
	Success     bool          `json:"success"`
	Error       error         `json:"-"`
	ErrStr      string        `json:"error,omitempty"`
	Steps       []Step        `json:"steps"`
	Elapsed     time.Duration `json:"elapsed"`
	Credentials *Credentials  `json:"credentials,omitempty"`
}

type OnStepFn func(domain string, step Step)

type Cloner struct {
	cfg    *config.Config
	client *ssh.Client
	sysMu  *sync.Mutex
}

func New(cfg *config.Config, client *ssh.Client, sysMu *sync.Mutex) *Cloner {
	return &Cloner{cfg: cfg, client: client, sysMu: sysMu}
}

func (c *Cloner) Clone(domain string, onStep OnStepFn) Result {
	started := time.Now()
	result := Result{Domain: domain}

	cfg := c.cfg.Clone

	siteUser := cfg.SiteUser(domain)
	siteName := config.SiteName(domain)
	webroot := cfg.Webroot(domain)
	chrootDir := cfg.ChrootDir(domain)
	sockPath := cfg.SockPath(domain)
	poolName := cfg.PoolName(domain)

	dbPass, err := genPassword(32)
	if err != nil {
		result.Error = fmt.Errorf("генерация db пароля: %w", err)
		result.ErrStr = result.Error.Error()
		return result
	}
	sftpPass, err := genPassword(32)
	if err != nil {
		result.Error = fmt.Errorf("генерация sftp пароля: %w", err)
		result.ErrStr = result.Error.Error()
		return result
	}

	creds := &Credentials{
		Domain:       domain,
		SiteUser:     siteUser,
		Webroot:      webroot,
		DBName:       siteName,
		DBUser:       siteName,
		DBPassword:   dbPass,
		SFTPPassword: sftpPass,
	}

	type stepDef struct {
		name   string
		skip   func() bool
		run    func() error
		serial bool
	}

	steps := []stepDef{
		{
			name:   "System user",
			serial: true,
			run:    func() error { return c.stepSystemUser(siteUser, sftpPass) },
		},
		{
			name: "Directories",
			run:  func() error { return c.stepDirs(chrootDir, webroot, siteUser) },
		},
		{
			name:   "PHP-FPM pool",
			serial: true,
			run:    func() error { return c.stepPHPFPMPool(poolName, siteUser, sockPath) },
		},
		{
			name: "FastCGI cache",
			run:  func() error { return c.stepFastCGICache() },
		},
		{
			name: "Nginx vhost",
			run:  func() error { return c.stepNginxVhost(domain, webroot, sockPath) },
		},
		{
			name: "MySQL database",
			run:  func() error { return c.stepMySQL(siteName, dbPass) },
		},
		{
			name: "Rsync files",
			run:  func() error { return c.stepRsync(webroot) },
		},
		{
			name: "wp-config.php",
			run:  func() error { return c.stepWPConfig(domain, webroot, siteName, dbPass) },
		},
		{
			name: "WP search-replace",
			run:  func() error { return c.stepSearchReplace(domain, webroot) },
		},
		{
			name: "Fix permissions",
			run:  func() error { return c.stepFixPerms(webroot, siteUser) },
		},
		{
			name: "Nginx reload",
			run:  func() error { return c.stepNginxReload() },
		},
		{
			name:   "SFTP chroot",
			serial: true,
			run:    func() error { return c.stepSFTPChroot(siteUser, chrootDir) },
		},
		{
			name: "Certbot SSL",
			skip: func() bool { return !c.cfg.Clone.Certbot },
			run:  func() error { return c.stepCertbot(domain) },
		},
	}

	for _, s := range steps {
		if s.skip != nil && s.skip() {
			step := Step{Name: s.name, Status: StatusSkipped}
			result.Steps = append(result.Steps, step)
			onStep(domain, step)
			continue
		}

		step := Step{Name: s.name, Status: StatusRunning}
		onStep(domain, step)

		stepStart := time.Now()

		var runErr error
		if s.serial {
			c.sysMu.Lock()
			runErr = s.run()
			c.sysMu.Unlock()
		} else {
			runErr = s.run()
		}

		step.Elapsed = time.Since(stepStart)

		if runErr != nil {
			step.Status = StatusFailed
			step.Error = runErr.Error()
			result.Steps = append(result.Steps, step)
			onStep(domain, step)
			result.Error = fmt.Errorf("[%s] %w", s.name, runErr)
			result.ErrStr = result.Error.Error()
			result.Elapsed = time.Since(started)
			return result
		}

		step.Status = StatusDone
		result.Steps = append(result.Steps, step)
		onStep(domain, step)
	}

	result.Success = true
	result.Credentials = creds
	result.Elapsed = time.Since(started)
	return result
}

// ── Шаги ──────────────────────────────────────────────────────────────────────

func (c *Cloner) stepSystemUser(siteUser, sftpPass string) error {
	res, _ := c.client.Run(fmt.Sprintf("id -u %s 2>/dev/null", siteUser))
	if res == nil || res.ExitCode != 0 {
		if _, err := c.client.RunSudo(fmt.Sprintf(
			"useradd --no-create-home --shell /usr/sbin/nologin --groups www-data %s",
			siteUser,
		)); err != nil {
			return fmt.Errorf("useradd: %w", err)
		}
	}
	if _, err := c.client.RunSudo(fmt.Sprintf(
		"bash -c \"echo '%s:%s' | chpasswd\"",
		siteUser, sftpPass,
	)); err != nil {
		return fmt.Errorf("chpasswd: %w", err)
	}
	return nil
}

func (c *Cloner) stepDirs(chrootDir, webroot, siteUser string) error {
	cmds := []string{
		fmt.Sprintf("mkdir -p %s", chrootDir),
		fmt.Sprintf("chown root:root %s", chrootDir),
		fmt.Sprintf("chmod 755 %s", chrootDir),
		fmt.Sprintf("mkdir -p %s", webroot),
		fmt.Sprintf("chown %s:www-data %s", siteUser, webroot),
		fmt.Sprintf("chmod 750 %s", webroot),
	}
	for _, cmd := range cmds {
		if _, err := c.client.RunSudo(cmd); err != nil {
			return err
		}
	}
	return nil
}

func (c *Cloner) stepPHPFPMPool(poolName, siteUser, sockPath string) error {
	phpVer := c.cfg.Clone.PHPVersion

	poolConf := fmt.Sprintf(`[%s]
user = %s
group = www-data

listen = %s
listen.owner = www-data
listen.group = www-data
listen.mode = 0660

pm = dynamic
pm.max_children = 5
pm.start_servers = 2
pm.min_spare_servers = 1
pm.max_spare_servers = 3

php_admin_value[upload_max_filesize] = 64M
php_admin_value[post_max_size] = 64M
php_admin_value[memory_limit] = 256M
php_admin_value[max_execution_time] = 300

php_admin_value[opcache.enable] = 1
php_admin_value[opcache.memory_consumption] = 128
php_admin_value[opcache.interned_strings_buffer] = 16
php_admin_value[opcache.max_accelerated_files] = 10000
php_admin_value[opcache.revalidate_freq] = 2
php_admin_value[opcache.save_comments] = 1
`, poolName, siteUser, sockPath)

	confPath := fmt.Sprintf("/etc/php/%s/fpm/pool.d/%s.conf", phpVer, poolName)

	// Пишем через sudo tee (cat > не работает с sudo)
	cmd := fmt.Sprintf(
		"echo '%s' | sudo tee %s > /dev/null",
		poolConf, confPath,
	)
	if _, err := c.client.RunOrFail(cmd); err != nil {
		return fmt.Errorf("запись pool конфига: %w", err)
	}
	if _, err := c.client.RunSudo(fmt.Sprintf("php-fpm%s --test 2>&1", phpVer)); err != nil {
		return fmt.Errorf("php-fpm --test: %w", err)
	}
	if _, err := c.client.RunSudo(fmt.Sprintf("systemctl restart php%s-fpm", phpVer)); err != nil {
		return fmt.Errorf("restart php-fpm: %w", err)
	}
	return nil
}

func (c *Cloner) stepFastCGICache() error {
	cfg := c.cfg.Clone
	cacheConf := fmt.Sprintf(`fastcgi_cache_path %s
    levels=1:2
    keys_zone=%s:100m
    inactive=60m
    max_size=1g;

fastcgi_cache_key "$scheme$request_method$host$request_uri";
`, cfg.NginxCachePath, cfg.NginxCacheZone)

	// Разбиваем на отдельные команды — && не работает через sudo
	if _, err := c.client.RunSudo(fmt.Sprintf("mkdir -p %s", cfg.NginxCachePath)); err != nil {
		return err
	}
	if _, err := c.client.RunSudo(fmt.Sprintf("chown www-data:www-data %s", cfg.NginxCachePath)); err != nil {
		return err
	}

	cmd := fmt.Sprintf(
		"echo '%s' | sudo tee /etc/nginx/conf.d/fastcgi_cache.conf > /dev/null",
		cacheConf,
	)
	if _, err := c.client.RunOrFail(cmd); err != nil {
		return fmt.Errorf("fastcgi_cache.conf: %w", err)
	}
	return nil
}

func (c *Cloner) stepNginxVhost(domain, webroot, sockPath string) error {
	cacheZone := c.cfg.Clone.NginxCacheZone

	vhostConf := fmt.Sprintf(`server {
    listen 80;
    server_name %s;

    root %s;
    index index.php index.html;

    client_max_body_size 64M;

    set $skip_cache 0;
    if ($request_method = POST)          { set $skip_cache 1; }
    if ($query_string != "")             { set $skip_cache 1; }
    if ($http_cookie ~* "wordpress_logged_in|wp-postpass|woocommerce") {
        set $skip_cache 1;
    }
    if ($request_uri ~* "/wp-admin/|/xmlrpc.php|wp-.*.php|/feed/|index.php|sitemap") {
        set $skip_cache 1;
    }

    location / {
        try_files $uri $uri/ /index.php?$args;
    }

    location ~* /(?:uploads|files)/.*\.php$ { deny all; }
    location ~ /\.ht                        { deny all; }
    location = /xmlrpc.php                  { deny all; }

    location ~ \.php$ {
        include snippets/fastcgi-php.conf;
        fastcgi_pass unix:%s;
        fastcgi_param SCRIPT_FILENAME $document_root$fastcgi_script_name;

        fastcgi_cache %s;
        fastcgi_cache_valid 200 60m;
        fastcgi_cache_valid 404 1m;
        fastcgi_cache_bypass $skip_cache;
        fastcgi_no_cache $skip_cache;
        fastcgi_cache_use_stale error timeout updating invalid_header http_500;
        fastcgi_cache_lock on;

        add_header X-Cache $upstream_cache_status always;
    }
}`, domain, webroot, sockPath, cacheZone)

	confPath := fmt.Sprintf("/etc/nginx/sites-available/%s", domain)
	cmd := fmt.Sprintf(
		"echo '%s' | sudo tee %s > /dev/null",
		vhostConf, confPath,
	)
	if _, err := c.client.RunOrFail(cmd); err != nil {
		return fmt.Errorf("запись nginx vhost: %w", err)
	}
	if _, err := c.client.RunSudo(fmt.Sprintf(
		"ln -sf /etc/nginx/sites-available/%s /etc/nginx/sites-enabled/%s",
		domain, domain,
	)); err != nil {
		return err
	}
	return nil
}

func (c *Cloner) stepMySQL(siteName, dbPass string) error {
	cmds := []string{
		fmt.Sprintf(
			`sudo mysql -uroot -e "CREATE DATABASE IF NOT EXISTS %s CHARACTER SET utf8mb4 COLLATE utf8mb4_unicode_ci;"`,
			siteName,
		),
		fmt.Sprintf(
			`sudo mysql -uroot -e "CREATE USER IF NOT EXISTS '%s'@'localhost' IDENTIFIED BY '%s';"`,
			siteName, dbPass,
		),
		fmt.Sprintf(
			`sudo mysql -uroot -e "GRANT ALL PRIVILEGES ON %s.* TO '%s'@'localhost'; FLUSH PRIVILEGES;"`,
			siteName, siteName,
		),
	}
	for _, cmd := range cmds {
		if _, err := c.client.RunOrFail(cmd); err != nil {
			return err
		}
	}
	return nil
}

func (c *Cloner) stepRsync(webroot string) error {
	_, err := c.client.RunSudo(fmt.Sprintf(
		"rsync -a --delete --exclude='wp-config.php' %s/ %s/",
		c.cfg.Source.WPPath, webroot,
	))
	return err
}

func (c *Cloner) stepWPConfig(domain, webroot, dbName, dbPass string) error {
	saltsRes, err := c.client.RunOrFail("curl -s https://api.wordpress.org/secret-key/1.1/salt/")
	if err != nil {
		return fmt.Errorf("получение WP солей: %w", err)
	}

	protocol := "http"
	if c.cfg.Clone.Certbot {
		protocol = "https"
	}

	wpConfig := fmt.Sprintf(`<?php

define( 'DB_NAME',     '%s' );
define( 'DB_USER',     '%s' );
define( 'DB_PASSWORD', '%s' );
define( 'DB_HOST',     'localhost' );
define( 'DB_CHARSET',  'utf8mb4' );
define( 'DB_COLLATE',  '' );

%s

$table_prefix = 'wp_';

define( 'WP_DEBUG', false );
define( 'WP_DEBUG_LOG', false );

define( 'WP_HOME',    '%s://%s' );
define( 'WP_SITEURL', '%s://%s' );

if ( ! defined( 'ABSPATH' ) ) {
    define( 'ABSPATH', __DIR__ . '/' );
}

require_once ABSPATH . 'wp-settings.php';
`, dbName, dbName, dbPass, saltsRes.Stdout, protocol, domain, protocol, domain)

	confPath := fmt.Sprintf("%s/wp-config.php", webroot)
	cmd := fmt.Sprintf(
		"echo '%s' | sudo tee %s > /dev/null",
		wpConfig, confPath,
	)
	if _, err := c.client.RunOrFail(cmd); err != nil {
		return fmt.Errorf("запись wp-config.php: %w", err)
	}
	return nil
}

func (c *Cloner) stepSearchReplace(domain, webroot string) error {
	src := c.cfg.Source.Domain
	cmd := fmt.Sprintf(
		"sudo wp search-replace '%s' '%s' --path='%s' --allow-root --skip-columns=guid 2>&1",
		src, domain, webroot,
	)
	if _, err := c.client.RunOrFail(cmd); err != nil {
		return fmt.Errorf("wp search-replace: %w", err)
	}
	c.client.Run(fmt.Sprintf(
		"sudo wp search-replace 'https://%s' 'https://%s' --path='%s' --allow-root --skip-columns=guid 2>&1",
		src, domain, webroot,
	))
	return nil
}

func (c *Cloner) stepFixPerms(webroot, siteUser string) error {
	cmds := []string{
		fmt.Sprintf("chown -R %s:www-data %s", siteUser, webroot),
		fmt.Sprintf("find %s -type d -exec chmod 755 {} +", webroot),
		fmt.Sprintf("find %s -type f -exec chmod 644 {} +", webroot),
		fmt.Sprintf("chmod 640 %s/wp-config.php", webroot),
	}
	for _, cmd := range cmds {
		if _, err := c.client.RunSudo(cmd); err != nil {
			return err
		}
	}
	return nil
}

func (c *Cloner) stepNginxReload() error {
	res, _ := c.client.RunSudo("nginx -t 2>&1")
	if res != nil && res.ExitCode != 0 {
		return fmt.Errorf("nginx -t: %s", res.Stdout)
	}
	if _, err := c.client.RunSudo("systemctl reload nginx"); err != nil {
		return err
	}
	return nil
}

func (c *Cloner) stepSFTPChroot(siteUser, chrootDir string) error {
	c.client.RunSudo(`sed -i 's/^#*PasswordAuthentication.*/PasswordAuthentication no/' /etc/ssh/sshd_config`)

	marker := fmt.Sprintf("SFTP CHROOT %s", siteUser)

	// Используем Run а не RunOrFail — grep возвращает exit 1 если не найдено
	res, _ := c.client.Run(fmt.Sprintf("sudo grep -c 'BEGIN %s' /etc/ssh/sshd_config", marker))
	if res != nil && res.ExitCode == 0 && res.Stdout != "0" {
		// блок уже есть — пропускаем
		return nil
	}

	c.client.RunSudo(fmt.Sprintf(
		`sed -i '/# BEGIN %s/,/# END %s/d' /etc/ssh/sshd_config`,
		marker, marker,
	))

	block := fmt.Sprintf(
		"\n# BEGIN %s\nMatch User %s\n    ChrootDirectory %s\n    ForceCommand internal-sftp\n    AllowTcpForwarding no\n    X11Forwarding no\n    PasswordAuthentication yes\n# END %s",
		marker, siteUser, chrootDir, marker,
	)
	cmd := fmt.Sprintf(`echo '%s' | sudo tee -a /etc/ssh/sshd_config > /dev/null`, block)
	if _, err := c.client.RunOrFail(cmd); err != nil {
		return fmt.Errorf("sshd_config: %w", err)
	}
	if _, err := c.client.RunSudo("sshd -t"); err != nil {
		return fmt.Errorf("sshd -t: %w", err)
	}
	if _, err := c.client.RunOrFail("sudo systemctl reload ssh || sudo systemctl reload sshd"); err != nil {
		return fmt.Errorf("reload ssh: %w", err)
	}
	return nil
}

func (c *Cloner) stepCertbot(domain string) error {
	cmd := fmt.Sprintf(
		"certbot --nginx -d %s --non-interactive --agree-tos --email %s 2>&1",
		domain, c.cfg.Clone.CertbotEmail,
	)
	res, err := c.client.RunSudo(cmd)
	if err != nil && res != nil {
		return fmt.Errorf("%w\n%s", err, res.Stdout)
	}
	return err
}

func (c *Cloner) mysqlAuth() string {
	return fmt.Sprintf("sudo mysql -uroot")
}

func genPassword(length int) (string, error) {
	b := make([]byte, length)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	raw := base64.StdEncoding.EncodeToString(b)
	var out strings.Builder
	for _, ch := range raw {
		if (ch >= 'a' && ch <= 'z') || (ch >= 'A' && ch <= 'Z') || (ch >= '0' && ch <= '9') {
			out.WriteRune(ch)
			if out.Len() >= length {
				break
			}
		}
	}
	return out.String(), nil
}
