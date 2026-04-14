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

## Requirements

### Local machine
- Go 1.22+
- `ssh` binary in PATH

### Remote server (Ubuntu)
- `nginx`
- `php{version}-fpm`
- `mysql` / `mysqldump`
- `rsync`
- `wp-cli` (available as `wp`)
- `certbot` (optional, for SSL)
- SSH user with passwordless `sudo`

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
    "nginx_cache_zone": "FASTCGI_CACHE"
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

# Or with custom flags
go run ./cmd/ -config prod.json -domains batch1.txt -workers 20
```

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


## License

MIT
 
