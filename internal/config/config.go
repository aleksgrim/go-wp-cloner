package config

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"
)

type Config struct {
	Server      ServerConfig `json:"server"`
	Source      SourceConfig `json:"source"`
	Clone       CloneConfig  `json:"clone"`
	Credentials CredConfig   `json:"credentials"`
}

type ServerConfig struct {
	Host    string `json:"host"`
	Port    int    `json:"port"`
	User    string `json:"user"`
	KeyPath string `json:"key_path"`
}

type SourceConfig struct {
	Domain string `json:"domain"`
	WPPath string `json:"wp_path"`
	DBName string `json:"db_name"`
	DBUser string `json:"db_user"`
	DBPass string `json:"db_pass"`
}

type CloneConfig struct {
	Workers        int    `json:"workers"`
	PHPVersion     string `json:"php_version"`
	UsernameSuffix string `json:"username_suffix"`
	DBRootPass     string `json:"db_root_pass"`
	Certbot        bool   `json:"certbot"`
	CertbotEmail   string `json:"certbot_email"`
	NginxCachePath string `json:"nginx_cache_path"`
	NginxCacheZone string `json:"nginx_cache_zone"`
}

type CredConfig struct {
	Dir string `json:"dir"`
}

// SiteUser: "alex2-site-1.g" → "alex2-site-1---admin"
func (c *CloneConfig) SiteUser(domain string) string {
	name := stripTLD(domain)
	suffix := c.UsernameSuffix
	if suffix == "" {
		suffix = "---admin"
	}
	return name + suffix
}

// SiteName: "alex2-site-1.g" → "alex2_site_1"  (db_name и db_user)
func SiteName(domain string) string {
	return strings.NewReplacer("-", "_", ".", "_").Replace(stripTLD(domain))
}

// Webroot: "/var/www/{site_user}/{domain}/public"
func (c *CloneConfig) Webroot(domain string) string {
	return fmt.Sprintf("/var/www/%s/%s/public", c.SiteUser(domain), domain)
}

// ChrootDir: "/var/www/{site_user}"
func (c *CloneConfig) ChrootDir(domain string) string {
	return fmt.Sprintf("/var/www/%s", c.SiteUser(domain))
}

// SockName: "php8.5-fpm-alex2-site-1"
func (c *CloneConfig) SockName(domain string) string {
	return fmt.Sprintf("php%s-fpm-%s", c.PHPVersion, stripTLD(domain))
}

// SockPath: "/run/php/php8.5-fpm-alex2-site-1.sock"
func (c *CloneConfig) SockPath(domain string) string {
	return fmt.Sprintf("/run/php/%s.sock", c.SockName(domain))
}

// PoolName == SiteUser
func (c *CloneConfig) PoolName(domain string) string {
	return c.SiteUser(domain)
}

func stripTLD(domain string) string {
	if idx := strings.LastIndex(domain, "."); idx != -1 {
		return domain[:idx]
	}
	return domain
}

func Load(path string) (*Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("не могу прочитать конфиг %s: %w", path, err)
	}

	var cfg Config
	if err := json.Unmarshal(data, &cfg); err != nil {
		return nil, fmt.Errorf("ошибка парсинга JSON: %w", err)
	}

	applyDefaults(&cfg)

	if err := cfg.validate(); err != nil {
		return nil, fmt.Errorf("ошибка конфига: %w", err)
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
			return fmt.Errorf("поле %q обязательно", field)
		}
	}
	if c.Clone.Certbot && c.Clone.CertbotEmail == "" {
		return fmt.Errorf("clone.certbot_email обязателен если certbot=true")
	}
	return nil
}

func LoadDomains(path string) ([]string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("не могу прочитать файл доменов %s: %w", path, err)
	}

	var domains []string
	seen := map[string]bool{}

	for i, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		if seen[line] {
			fmt.Fprintf(os.Stderr, "⚠️  строка %d: дубликат %q — пропущен\n", i+1, line)
			continue
		}
		seen[line] = true
		domains = append(domains, line)
	}

	if len(domains) == 0 {
		return nil, fmt.Errorf("файл доменов пустой или все строки закомментированы")
	}

	return domains, nil
}