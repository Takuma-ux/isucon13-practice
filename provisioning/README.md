# Provisioning (ISUCON13)

Config in this directory is **inert** until copied or applied on the **competition EC2** host. Do not run apply scripts on a developer laptop except **dry-run** modes.

**3-server handoff:** `docs/16-3-server-provisioning-handoff.md` — roles `app` / `db` / `combined` (`ISUCON_HOST_ROLE`).

## Safety (read first)

| Action | Guard |
|--------|--------|
| `apply.sh` (sysctl, timers, systemd drop-in; role-aware) | `CONFIRM_INFRA_APPLY=yes` |
| `apply.sh` preview | `INFRA_DRY_RUN=1` (no `/etc` writes, no reloads) |
| `apply-nginx.sh` (app/combined; HTTP/2 vhost + conf.d) | `CONFIRM_NGINX_APPLY=yes` (also set on `apply.sh` to run in same pass) |
| Nginx apply preview | `NGINX_APPLY_DRY_RUN=1` |
| `apply-mysql-tuning.sh` | `CONFIRM_MYSQL_TUNING=yes` or interactive `yes`; **uses this machine's RAM** |
| MySQL preview | `MYSQL_TUNING_DRY_RUN=1` |
| MySQL slow log (diagnostic bench) | `CONFIRM_SLOW_LOG=yes` + `apply-mysql-slow-log.sh on\|off`; preview `MYSQL_SLOW_LOG_DRY_RUN=1` |
| Nginx reload | **Manual:** `nginx -t` then `CONFIRM_NGINX_RELOAD=yes systemctl reload nginx` (script does not auto-reload) |
| Full deploy order | `./provisioning/deploy-checklist.sh` (checklist); `--preflight` / `--dry-run` |
| Drop unused `ng_words_word` index (existing DB) | `CONFIRM_DROP_NG_WORDS_WORD=yes ./scripts/drop-ng_words-word-index.sh`; check only: `--check` |

### Nginx HTTP/2

`provisioning/nginx/isupipe.conf` uses `listen 443 ssl http2;` (nginx 1.18 on Ubuntu 22.04). On **nginx ≥ 1.25.1**, replace with `listen 443 ssl;` and add `http2 on;` inside the `server` block. Run `nginx -V` on the host before reload.

### TCP BBR

`apply.sh` installs `99-isucon-bbr.conf` only when `bbr` appears in `/proc/sys/net/ipv4/tcp_available_congestion_control` (typical on competition Ubuntu; often absent on minimal dev kernels). Sets `net.core.default_qdisc=fq` and `tcp_congestion_control=bbr`. Check: `sysctl net.ipv4.tcp_congestion_control`.

### Sysctl / `vm.swappiness` (explicit precedence)

| File | Host roles | `vm.swappiness` |
|------|------------|-----------------|
| `sysctl/99-isucon.conf` | all | *(not set — network only)* |
| `sysctl/99-isucon-db.conf` | `db` only | `1` |
| `sysctl/100-isucon-swappiness.conf` | `app`, `combined` | `10` (from `100-isucon-swappiness-app.conf`) |

`apply.sh` installs one swappiness drop-in and **removes** the other role’s file. Do not hand-edit both on one host.

**Never** restart MySQL or reload nginx locally to “test” tuning. Use dry-run and inspect generated config only.

### MySQL buffer pool incident

`innodb_buffer_pool_size` is computed from **local** `MemTotal`. Running apply on a large laptop can allocate tens of GiB to MySQL and destabilize the system. EC2 only for real apply.

## Nginx

| File | Purpose |
|------|---------|
| `nginx/isupipe.conf` | TLS vhost: session cache/tickets, `access_log off`, static `/assets/` cache, `/api` proxy + buffers + upstream keepalive |
| `nginx/nginx-main-snippet.conf` | `worker_processes auto`, `worker_rlimit_nofile 65535`, `worker_connections 4096` — merge into `/etc/nginx/nginx.conf` |
| `nginx/conf.d/isucon-http-tuning.conf` | `keepalive_timeout 65` — copy to `/etc/nginx/conf.d/` |
| `nginx/conf.d/isucon-alp-logging.conf` | `log_format isucon_ltsv` for alp — copy to `/etc/nginx/conf.d/` |
| `nginx/isupipe.access-log-snippet.conf` | One-line `access_log` to swap in for a diagnostic bench run |

Official reference vhost had **no** `access_log` override (global access log stayed on). This repo disables access logging on the app vhost explicitly.

After merging configs on the server:

```bash
sudo nginx -t
# only when intentional:
sudo CONFIRM_NGINX_RELOAD=yes systemctl reload nginx
```

## MySQL generated drop-in

`apply-mysql-tuning.sh` writes `/etc/mysql/mysql.conf.d/isucon-tuning.cnf` with:

- `innodb_buffer_pool_size` (45% of MemTotal → 128MiB steps → **round up** to InnoDB `chunk_size×instances`; see `lib/mysql-pool-math.sh`)
- `innodb_flush_log_at_trx_commit = 2`
- `innodb_flush_neighbors = 0` (SSD/EBS; no adjacent-page flush benefit)
- `innodb_flush_method = O_DIRECT` (InnoDB buffer pool is primary cache; safe on competition Ubuntu **ext4** root — confirm with `findmnt /` and `@@innodb_flush_method` before apply)
- `innodb_io_capacity = 2000`, `innodb_io_capacity_max = 4000` (raise from HDD-oriented defaults; not a substitute for correct queries)
- `innodb_lock_wait_timeout = 10` (default 50s)
- `performance_schema = OFF`
- `table_open_cache = 4096`, `thread_cache_size = 64`
- `log_bin = OFF`, `general_log = OFF` (single-node; avoids binlog DML overhead and accidental full query logging)
- `skip_name_resolve`, `max_connections = 500`

Verify (read-only): `./provisioning/verify-mysql-tuning.sh`

## MySQL slow query log (diagnostic bench only)

Overhead if left on — enable only for one analysis run, then disable.

```bash
# Before bench
sudo MYSQL_SLOW_LOG_DRY_RUN=1 ./provisioning/apply-mysql-slow-log.sh on   # preview
sudo CONFIRM_SLOW_LOG=yes ./provisioning/apply-mysql-slow-log.sh on     # writes drop-in + restarts MySQL

# After bench
sudo pt-query-digest /var/log/mysql/isucon-slow.log

# Turn off (removes drop-in + restarts MySQL)
sudo CONFIRM_SLOW_LOG=yes ./provisioning/apply-mysql-slow-log.sh off
```

Drop-in: `/etc/mysql/mysql.conf.d/isucon-slow-log.cnf` (`long_query_time = 0.05`, log file `/var/log/mysql/isucon-slow.log`).

## Nginx access log for alp (diagnostic bench only)

1. Install `conf.d/isucon-alp-logging.conf` (defines `isucon_ltsv` LTSV format).
2. In the isupipe vhost, replace `access_log off;` with the line from `isupipe.access-log-snippet.conf`.
3. `sudo nginx -t` && reload with `CONFIRM_NGINX_RELOAD=yes`.
4. Run bench; analyze: `alp json --file=/var/log/nginx/isucon.access.ltsv` (or `alp ltsv ...` depending on alp version).
5. Restore `access_log off;` and reload.

Reference upstream had no custom alp format; default `combined` is not ideal for alp.

## Filesystem `noatime`

ISUCON ansible in the official repo does **not** set `noatime` on `/`. Typical Ubuntu EC2 root mounts use **`relatime`** (see `mount` / `/proc/mounts`), not `noatime`.

**Manual step on the competition host** (requires reboot or remount; not automated here):

1. Edit `/etc/fstab`: add `noatime` (or `relatime` → `noatime`) to the root filesystem options, e.g.  
   `UUID=... / ext4 defaults,noatime,errors=remount-ro 0 1`
2. `sudo mount -o remount,noatime /` (if remount without reboot is acceptable), or reboot during a maintenance window.

Skipping atime updates reduces metadata writes on static file reads under `public/` and nginx `sendfile`.

## App build (PGO + GOAMD64)

- **PGO:** `go/PGO.md` — `default.pgo` in `go/` (auto-used by `go build`); capture during bench via `:6060` or `./go/scripts/capture-pgo.sh`.
- **CPU level:** `go/BUILD.md` — default **`GOAMD64=v2`** on unknown EC2; verify `avx2` in `/proc/cpuinfo` before `v3`.

## Full server apply (EC2)

```bash
# Preview (any role)
sudo INFRA_DRY_RUN=1 ISUCON_HOST_ROLE=app ./provisioning/apply.sh

# App server (srv1 / srv2) — identical on both
sudo CONFIRM_INFRA_APPLY=yes CONFIRM_NGINX_APPLY=yes ISUCON_HOST_ROLE=app \
  ./provisioning/apply.sh
sudo nginx -t && sudo CONFIRM_NGINX_RELOAD=yes systemctl reload nginx

# DB server (srv3)
sudo CONFIRM_INFRA_APPLY=yes CONFIRM_MYSQL_TUNING=yes ISUCON_HOST_ROLE=db \
  ./provisioning/apply.sh

# Single VM (stock layout)
sudo CONFIRM_INFRA_APPLY=yes CONFIRM_MYSQL_TUNING=yes CONFIRM_NGINX_APPLY=yes \
  ISUCON_HOST_ROLE=combined ./provisioning/apply.sh
```

`apply.sh` runs MySQL tuning only on `db` / `combined`. App-only hosts skip MySQL restart.

Local unit check (no MySQL): `./provisioning/scripts/test-mysql-pool-math.sh`
