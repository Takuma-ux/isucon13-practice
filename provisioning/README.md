# Provisioning (ISUCON13 app server)

Config in this directory is **inert** until copied or applied on the **competition EC2** host. Do not run apply scripts on a developer laptop except **dry-run** modes.

## Safety (read first)

| Action | Guard |
|--------|--------|
| `apply.sh` (sysctl, timers, systemd drop-in) | `CONFIRM_INFRA_APPLY=yes` |
| `apply.sh` preview | `INFRA_DRY_RUN=1` (no `/etc` writes, no reloads) |
| `apply-mysql-tuning.sh` | `CONFIRM_MYSQL_TUNING=yes` or interactive `yes`; **uses this machine's RAM** |
| MySQL preview | `MYSQL_TUNING_DRY_RUN=1` |
| Nginx reload | **Manual:** `nginx -t` then `CONFIRM_NGINX_RELOAD=yes systemctl reload nginx` (script does not auto-reload) |

**Never** restart MySQL or reload nginx locally to “test” tuning. Use dry-run and inspect generated config only.

### MySQL buffer pool incident

`innodb_buffer_pool_size` is computed from **local** `MemTotal`. Running apply on a large laptop can allocate tens of GiB to MySQL and destabilize the system. EC2 only for real apply.

## Nginx

| File | Purpose |
|------|---------|
| `nginx/isupipe.conf` | TLS vhost: session cache/tickets, `access_log off`, static `/assets/` cache, `/api` proxy + buffers + upstream keepalive |
| `nginx/nginx-main-snippet.conf` | `worker_processes auto`, `worker_rlimit_nofile 65535`, `worker_connections 4096` — merge into `/etc/nginx/nginx.conf` |
| `nginx/conf.d/isucon-http-tuning.conf` | `keepalive_timeout 65` — copy to `/etc/nginx/conf.d/` |

Official reference vhost had **no** `access_log` override (global access log stayed on). This repo disables access logging on the app vhost explicitly.

After merging configs on the server:

```bash
sudo nginx -t
# only when intentional:
sudo CONFIRM_NGINX_RELOAD=yes systemctl reload nginx
```

## MySQL generated drop-in

`apply-mysql-tuning.sh` writes `/etc/mysql/mysql.conf.d/isucon-tuning.cnf` with:

- `innodb_buffer_pool_size` (from MemTotal, 45% policy)
- `innodb_flush_log_at_trx_commit = 2`
- `performance_schema = OFF`
- `table_open_cache = 4096`, `thread_cache_size = 64`
- `log_bin = OFF`, `general_log = OFF` (single-node; avoids binlog DML overhead and accidental full query logging)
- `skip_name_resolve`, `max_connections = 500`

Verify (read-only): `./provisioning/verify-mysql-tuning.sh`

## Filesystem `noatime`

ISUCON ansible in the official repo does **not** set `noatime` on `/`. Typical Ubuntu EC2 root mounts use **`relatime`** (see `mount` / `/proc/mounts`), not `noatime`.

**Manual step on the competition host** (requires reboot or remount; not automated here):

1. Edit `/etc/fstab`: add `noatime` (or `relatime` → `noatime`) to the root filesystem options, e.g.  
   `UUID=... / ext4 defaults,noatime,errors=remount-ro 0 1`
2. `sudo mount -o remount,noatime /` (if remount without reboot is acceptable), or reboot during a maintenance window.

Skipping atime updates reduces metadata writes on static file reads under `public/` and nginx `sendfile`.

## Full server apply (EC2)

```bash
sudo INFRA_DRY_RUN=1 ./provisioning/apply.sh          # preview
sudo CONFIRM_INFRA_APPLY=yes CONFIRM_MYSQL_TUNING=yes ./provisioning/apply.sh
```

`apply.sh` sources `apply-mysql-tuning.sh`; both confirmations are required for a full run including MySQL restart.
