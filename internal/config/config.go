package config

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"
)

// Config represents the full application configuration structure.
type Config struct {
	Server      ServerConfig `json:"server"`
	Source      SourceConfig `json:"source"`
	Clone       CloneConfig  `json:"clone"`
	Credentials CredConfig   `json:"credentials"`
}

// ServerConfig contains target server SSH connection details.
type ServerConfig struct {
	Host    string `json:"host"`
	Port    int    `json:"port"`
	User    string `json:"user"`
	KeyPath string `json:"key_path"`
}

// SourceConfig contains the configuration for the source WordPress site to be cloned.
type SourceConfig struct {
	Domain string `json:"domain"`
	WPPath string `json:"wp_path"`
	DBName string `json:"db_name"`
	DBUser string `json:"db_user"`
	DBPass string `json:"db_pass"`
}

// CloneConfig contains general settings for the cloning process and isolation target.
type CloneConfig struct {
	Workers           int    `json:"workers"`
	PHPVersion        string `json:"php_version"`
	UsernameSuffix    string `json:"username_suffix"`
	DBRootPass        string `json:"db_root_pass"`
	Certbot           bool   `json:"certbot"`
	CertbotEmail      string `json:"certbot_email"`
	NginxCachePath    string `json:"nginx_cache_path"`
	NginxCacheZone    string `json:"nginx_cache_zone"`
	CommandTimeoutSec int    `json:"command_timeout_sec"`
}

// CredConfig contains local storage settings for generated passwords.
type CredConfig struct {
	Dir string `json:"dir"`
}

// SiteUser returns the Linux system username for a given domain.
func (c *CloneConfig) SiteUser(domain string) string {
	name := stripTLD(domain)
	suffix := c.UsernameSuffix
	if suffix == "" {
		suffix = "---admin"
	}
	return name + suffix
}

// SiteName returns a sanitized name suitable for database and database user names.
func SiteName(domain string) string {
	return strings.NewReplacer("-", "_", ".", "_").Replace(stripTLD(domain))
}

// Webroot returns the absolute path to the site's public directory.
func (c *CloneConfig) Webroot(domain string) string {
	return fmt.Sprintf("/var/www/%s/%s/public", c.SiteUser(domain), domain)
}

// ChrootDir returns the absolute path to the directory that will be used for SFTP chroot.
func (c *CloneConfig) ChrootDir(domain string) string {
	return fmt.Sprintf("/var/www/%s", c.SiteUser(domain))
}

// SockName returns the name of the PHP-FPM socket file.
func (c *CloneConfig) SockName(domain string) string {
	return fmt.Sprintf("php%s-fpm-%s", c.PHPVersion, stripTLD(domain))
}

// SockPath returns the absolute path to the PHP-FPM socket.
func (c *CloneConfig) SockPath(domain string) string {
	return fmt.Sprintf("/run/php/%s.sock", c.SockName(domain))
}

// PoolName returns the name of the PHP-FPM pool, which matches the system username.
func (c *CloneConfig) PoolName(domain string) string {
	return c.SiteUser(domain)
}

func stripTLD(domain string) string {
	if idx := strings.LastIndex(domain, "."); idx != -1 {
		return domain[:idx]
	}
	return domain
}

// Load reads a JSON configuration file from the given path, applies defaults, and validates fields.
func Load(path string) (*Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("cannot read config %s: %w", path, err)
	}

	var cfg Config
	if err := json.Unmarshal(data, &cfg); err != nil {
		return nil, fmt.Errorf("JSON parsing error: %w", err)
	}

	applyDefaults(&cfg)

	if err := cfg.validate(); err != nil {
		return nil, fmt.Errorf("config error: %w", err)
	}

	return &cfg, nil
}

func applyDefaults(cfg *Config) {
	if cfg.Server.Port == 0 {
		cfg.Server.Port = 22
	}
	if cfg.Clone.Workers == 0 {
		cfg.Clone.Workers = 5
	}
	if cfg.Clone.PHPVersion == "" {
		cfg.Clone.PHPVersion = "8.5"
	}
	if cfg.Clone.NginxCachePath == "" {
		cfg.Clone.NginxCachePath = "/var/cache/nginx/fastcgi"
	}
	if cfg.Clone.NginxCacheZone == "" {
		cfg.Clone.NginxCacheZone = "FASTCGI_CACHE"
	}
	if cfg.Credentials.Dir == "" {
		cfg.Credentials.Dir = "./credentials"
	}
	if cfg.Clone.CommandTimeoutSec == 0 {
		cfg.Clone.CommandTimeoutSec = 600 // 10 minutes per command
	}
}

func (c *Config) validate() error {
	required := map[string]string{
		"server.host":        c.Server.Host,
		"server.user":        c.Server.User,
		"server.key_path":    c.Server.KeyPath,
		"source.domain":      c.Source.Domain,
		"source.wp_path":     c.Source.WPPath,
		"source.db_name":     c.Source.DBName,
		"source.db_user":     c.Source.DBUser,
		"source.db_pass":     c.Source.DBPass,
		"clone.db_root_pass": c.Clone.DBRootPass,
	}
	for field, val := range required {
		if val == "" {
			return fmt.Errorf("field %q is required", field)
		}
	}
	if c.Clone.Certbot && c.Clone.CertbotEmail == "" {
		return fmt.Errorf("clone.certbot_email is required if certbot=true")
	}
	return nil
}

// LoadDomains reads a list of domains from a text file, ignoring empty lines and comments.
func LoadDomains(path string) ([]string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("cannot read domains file %s: %w", path, err)
	}

	var domains []string
	seen := map[string]bool{}

	for i, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		if seen[line] {
			fmt.Fprintf(os.Stderr, "⚠️  line %d: duplicate %q — skipped\n", i+1, line)
			continue
		}
		seen[line] = true
		domains = append(domains, line)
	}

	if len(domains) == 0 {
		return nil, fmt.Errorf("domains file is empty or all lines are commented out")
	}

	return domains, nil
}