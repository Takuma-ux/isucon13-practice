# 3-server provisioning handoff

Topology (team split):

| Host | Role | `ISUCON_HOST_ROLE` | Provisioning |
|------|------|-------------------|--------------|
| srv1 | App + nginx | `app` | `apply.sh` + `CONFIRM_NGINX_APPLY=yes` |
| srv2 | App + nginx | `app` | **Same as srv1** (identical nginx/sysctl/systemd) |
| srv3 | MySQL only | `db` | `apply.sh` (no nginx, no isupipe limits) |

Stock single-VM practice / AMI layout: omit role or use `combined`.

## Fixed gaps (this doc)

### 1. `vm.swappiness` conflict

**Problem:** `99-isucon-db.conf` set `swappiness=1`, but `99-isucon.conf` also set `swappiness=10`. Under `sysctl.d` lex order, `99-isucon.conf` loads **after** `99-isucon-db.conf` and overwrote DB to `10`.

**Fix:**

- `99-isucon.conf` — network tuning only (no `vm.swappiness`).
- `99-isucon-db.conf` — installed **only** when `ISUCON_HOST_ROLE=db` (`swappiness=1`).
- `100-isucon-swappiness.conf` — installed **only** on `app` / `combined` from `100-isucon-swappiness-app.conf` (`swappiness=10`), loads after all `99-isucon*`.
- `apply.sh` removes the opposite role’s drop-in so a host never keeps both.

Verify on host: `sysctl vm.swappiness` → **1** on srv3, **10** on srv1/srv2.

### 2. InnoDB buffer pool rounding (2560M → 2048M live)

**Problem:** `innodb_buffer_pool_size=2560M` in cnf rounded **down** to `2048M` at runtime (128M chunk × 8 instances ⇒ 1G granularity).

**Fix:** `provisioning/lib/mysql-pool-math.sh` rounds the **written** value up to the next multiple of `chunk_size × instances` (defaults: 128M × 8 when pool ≥ 1G). Cnf comments record `policy_raw` vs `effective_innodb`.

Example: policy `2560M` → cnf `3072M` → live `3072M`. The extra RAM vs naive 45% is alignment only; see `NOTE` line from `verify-mysql-tuning.sh`.

### 3. App server provisioning parity (srv1 vs srv2)

**Problem:** srv2 missed HTTP/2 vhost and other nginx steps applied manually on srv1.

**Fix:** `provisioning/apply-nginx.sh` installs the same `conf.d` snippets, `sites-enabled/isupipe` (with `listen 443 ssl http2`), and idempotent `nginx.conf` worker markers on **every** `app` / `combined` host when `CONFIRM_NGINX_APPLY=yes` is set on `apply.sh`.

Run the **same** command on srv1 and srv2:

```bash
sudo CONFIRM_INFRA_APPLY=yes CONFIRM_MYSQL_TUNING=yes CONFIRM_NGINX_APPLY=yes \
  ISUCON_HOST_ROLE=app ./provisioning/apply.sh
sudo nginx -t && sudo CONFIRM_NGINX_RELOAD=yes systemctl reload nginx
```

DB host:

```bash
sudo CONFIRM_INFRA_APPLY=yes CONFIRM_MYSQL_TUNING=yes \
  ISUCON_HOST_ROLE=db ./provisioning/apply.sh
```

## Live EC2 verification checklist (when instances return)

1. **Swappiness:** on srv3 `sysctl vm.swappiness` = 1; on srv1/srv2 = 10.
2. **Buffer pool:** on srv3 `./provisioning/verify-mysql-tuning.sh` — `OK: live buffer pool matches effective cnf`; cnf `effective_innodb` ≥ `policy_raw`.
3. **Nginx parity:** on srv1 and srv2, `nginx -T 2>/dev/null | grep -E 'http2|worker_connections 4096|isucon-http-tuning'` — same matches on both.
4. **HTTP/2:** `curl -sI --http2 https://pipe.u.isucon.local/...` (or team URL) shows HTTP/2 on both app hosts.

Cannot verify without live hosts: actual `nginx -t` on competition TLS paths, BBR module presence, and full bench under 3-server split.
