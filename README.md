# go-wp-cloner

Mass WordPress site cloning tool written in Go. Clone one WP site to 50–200 domains in parallel — each with its own system user, PHP-FPM pool, MySQL database, nginx vhost with FastCGI cache and SFTP access.

## How it works

You have one source WordPress site. You run one command. The tool creates fully isolated copies on as many domains as you need — in parallel.

Each clone gets:
- System user (`domain---admin`) with no shell, SFTP only
- PHP-FPM pool with dedicated socket running as that user
- MySQL database and user with a random 32-char password
- Nginx vhost with FastCGI cache (matches your Ansible setup exactly)
- SFTP chroot access via sshd_config Match block
- `wp-config.php` with fresh WordPress salts from the WP API
- Credentials saved locally to `credentials/{domain}/credentials.txt`

## Server Requirements

### Operating System
- Ubuntu 22.04 LTS or Ubuntu 24.04 LTS
- Other Debian-based distros may work but are not tested

### SSH Access
- SSH key-based auth (password auth for the deploy user must work too)
- Deploy user must have **passwordless sudo**:
```bash
  echo "youruser ALL=(ALL) NOPASSWD: ALL" >> /etc/sudoers
```
- SSH port must be open in firewall
- `BatchMode` compatible — no interactive prompts during connection

### Required packages
Install all dependencies:
```bash
apt update && apt install -y \
  nginx \
  mysql-server \
  rsync \
  curl \
  php8.5-fpm \
  php8.5-mysql \
  php8.5-curl \
  php8.5-gd \
  php8.5-mbstring \
  php8.5-xml \
  php8.5-zip \
  php8.5-intl
```

Install WP-CLI:
```bash
curl -O https://raw.githubusercontent.com/wp-cli/builds/gh-pages/phar/wp-cli.phar
chmod +x wp-cli.phar
mv wp-cli.phar /usr/local/bin/wp
```

### MySQL
- MySQL root password must be set and added to `config.json` as `db_root_pass`
- Root user must authenticate via password (not only unix socket):
```bash
  sudo mysql -uroot
  ALTER USER 'root'@'localhost' IDENTIFIED WITH mysql_native_password BY 'yourpassword';
  FLUSH PRIVILEGES;
```

### Nginx
- Default nginx install is fine
- `sites-available` / `sites-enabled` structure must exist (default on Ubuntu)
- FastCGI cache directory will be created automatically at the path specified in config (`/var/cache/nginx/fastcgi` by default)

### PHP-FPM
- Version must match `php_version` in `config.json`
- Default pool `www.conf` can stay — the tool creates additional per-site pools
- `www-data` group must exist (default on Ubuntu)

### SFTP / SSH
- `internal-sftp` subsystem must be enabled in `/etc/ssh/sshd_config`:
```
  Subsystem sftp internal-sftp
```
  This is the default on Ubuntu — no changes needed
- The tool adds `Match User` blocks to `sshd_config` automatically

### Source WordPress site
The source site must be fully working before cloning:
- Files at the path specified in `source.wp_path`
- Database accessible with credentials in `source.db_name`, `source.db_user`, `source.db_pass`
- WP-CLI must be able to connect: `sudo wp db check --path=/your/source/path --allow-root`

### Firewall
Open required ports:
```bash
ufw allow 22    # or your custom SSH port
ufw allow 80    # HTTP
ufw allow 443   # HTTPS (if using Certbot)
```

### Local machine (where you run wp-cloner)
- Go 1.22+ (only needed to build from source)
- `ssh` binary in PATH
- SSH private key with access to the server

## Install

```bash
git clone https://github.com/aleksgrim/go-wp-cloner.git
cd go-wp-cloner
make build
```

Or download a prebuilt binary from [Releases](https://github.com/aleksgrim/go-wp-cloner/releases).

## Setup

**1. Config:**
```bash
cp config.json.example config.json
```

```json
{
  "server": {
    "host": "your-server-ip",
    "port": 22,
    "user": "deploy",
    "key_path": "~/.ssh/id_rsa"
  },
  "source": {
    "domain": "source-site.com",
    "wp_path": "/var/www/source-site---admin/source-site.com/public",
    "db_name": "source_site",
    "db_user": "source_site",
    "db_pass": "source_db_password"
  },
  "clone": {
    "workers": 10,
    "php_version": "8.5",
    "username_suffix": "---admin",
    "db_root_pass": "mysql_root_password",
    "certbot": false,
    "certbot_email": "",
    "nginx_cache_path": "/var/cache/nginx/fastcgi",
    "nginx_cache_zone": "FASTCGI_CACHE",
    "command_timeout_sec": 600,
    "log_retention_days": 30
  },
  "credentials": {
    "dir": "./credentials"
  }
}
```

**2. Domain list:**
```bash
cp domains.txt.example domains.txt
# edit domains.txt — one domain per line, # for comments
```

```
domain1.com
domain2.com
# domain3.com  ← skipped
domain4.com
```

## Usage

```bash
# Check SSH connection and tools on the server
make test-conn

# Preview what will be created — no changes made
make dry-run

# Run cloning
make run

# Remove sites listed in domains.txt
make remove

# Remove without confirmation prompt (for scripting)
make remove-force

# Or with custom flags
go run ./cmd/ -config prod.json -domains batch1.txt -workers 20
```

## Removing Sites

The `-remove` flag tears down everything that was created for each domain in `domains.txt`.
Useful for cleaning up after a failed batch or decommissioning sites.

```bash
# Interactive — shows a summary and asks for 'yes'
make remove

# Non-interactive — skips the confirmation prompt
make remove-force

# Custom domains file
go run ./cmd/ -remove -domains failed.txt
```

What gets removed per domain (in order):

| Step | What |
|------|------|
| Nginx vhost | removes symlink + config, reloads nginx |
| PHP-FPM pool | removes pool config, restarts php-fpm |
| MySQL DB & user | `DROP DATABASE IF EXISTS`, `DROP USER IF EXISTS` |
| Files & dirs | `rm -rf` webroot and chroot directory |
| System user | kills lingering processes, then `userdel` |
| SSH chroot block | removes `Match User` block from sshd_config, reloads sshd |
| Local credentials | removes `credentials/{domain}/` directory |

> Steps are **best-effort** — if one fails, the rest still run. The summary shows which domains had partial cleanup.

All removal activity is logged to `logs/YYYY-MM-DD.log` alongside clone operations.

## What gets created per domain

Given `myshop.com`:

| What | Value |
|---|---|
| System user | `myshop---admin` |
| Chroot dir | `/var/www/myshop---admin` |
| Webroot | `/var/www/myshop---admin/myshop.com/public` |
| PHP-FPM pool | `/etc/php/8.5/fpm/pool.d/myshop---admin.conf` |
| PHP socket | `/run/php/php8.5-fpm-myshop.sock` |
| Nginx vhost | `/etc/nginx/sites-available/myshop.com` |
| MySQL DB | `myshop` |
| MySQL user | `myshop` + random password |
| SFTP password | random 32-char password |
| Credentials | `./credentials/myshop.com/credentials.txt` |

## Naming conventions

**System user** — strip TLD, append suffix:
```
myshop.com      → myshop---admin
my-shop.store   → my-shop---admin
alex-site-1.g   → alex-site-1---admin
```

**DB name** — strip TLD, replace `-` and `.` with `_`:
```
myshop.com      → myshop
my-shop.store   → my_shop
alex-site-1.g   → alex_site_1
```

## Parallelism

All domains are processed in parallel up to `workers` limit. Operations that can't run concurrently on Linux are serialized automatically:

- `useradd` — locks `/etc/passwd` and `/etc/group`
- `php-fpm restart` — can't restart in parallel
- `sshd_config` — can't write in parallel

Everything else (rsync, mysql, nginx, wp-cli) runs fully parallel.

## Idempotent runs

Safe to re-run on existing domains — nothing breaks:

- `useradd` — skipped if user already exists
- `CREATE DATABASE IF NOT EXISTS` ✅
- `CREATE USER IF NOT EXISTS` ✅
- SFTP chroot block — skipped if already present in sshd_config ✅
- rsync — idempotent by nature ✅

## Credentials

After cloning, credentials are saved locally:

```
credentials/
  myshop.com/
    credentials.txt
  domain2.com/
    credentials.txt
```

Each file:
```
=== myshop.com ===
Created: 2026-04-14 12:00:00

SFTP:
  User:     myshop---admin
  Password: xK9mP2...

MySQL:
  DB:       myshop
  User:     myshop
  Password: rT4nQ7...

WordPress:
  URL:      http://myshop.com
  Admin:    http://myshop.com/wp-admin
```

> ⚠️ `credentials/` is in `.gitignore` — never commit passwords.

## Logging

Every run writes a timestamped log to `logs/YYYY-MM-DD.log`:

```
2026-04-14 20:15:46.123 [INFO ] === wp-cloner v0.3.0 started — 150 domains, 10 workers ===
2026-04-14 20:15:46.201 [START] [example.com] cloning started
2026-04-14 20:15:46.210 [STEP ] [example.com] RUNNING  System user
2026-04-14 20:15:47.850 [STEP ] [example.com] OK       System user            1.6s
2026-04-14 20:15:47.860 [STEP ] [example.com] RUNNING  MySQL database
2026-04-14 20:15:49.300 [STEP ] [example.com] FAILED   MySQL database         Access denied...
2026-04-14 20:15:49.310 [FAIL ] [example.com] ✗ failed in 3.1s — [MySQL database] Access denied...
...
2026-04-14 21:00:00.000 [SUMRY] batch finished — total=150 ok=148 err=2 elapsed=44m12s
```

Log files older than `log_retention_days` days (default: 30) are deleted automatically on each run.

`logs/` is in `.gitignore`.


## License

MIT
 
