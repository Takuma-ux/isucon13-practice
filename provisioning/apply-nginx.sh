#!/usr/bin/env bash
# Install repo nginx tuning on every app-facing host (srv1/srv2/combined).
# Same files on each app server — avoids accidental HTTP/2 / keepalive drift.
#
# Preview:  NGINX_APPLY_DRY_RUN=1 sudo ./provisioning/apply-nginx.sh
# Apply:    sudo CONFIRM_NGINX_APPLY=yes ./provisioning/apply-nginx.sh
# Reload:   sudo nginx -t && sudo CONFIRM_NGINX_RELOAD=yes systemctl reload nginx
set -euo pipefail

ROOT="$(cd "$(dirname "$0")/.." && pwd)"
NGINX_DIR="$ROOT/provisioning/nginx"
VHOST_DST=/etc/nginx/sites-enabled/isupipe

# shellcheck disable=SC1091
source "$ROOT/provisioning/lib/host-role.sh"
resolve_host_role

if ! host_role_is_app_surface; then
	echo "Skip nginx apply: ISUCON_HOST_ROLE=${ISUCON_HOST_ROLE:-combined} (not an app surface)" >&2
	exit 0
fi

apply_nginx_dry_run() {
	echo "[dry-run] Would copy conf.d/isucon-http-tuning.conf -> /etc/nginx/conf.d/"
	echo "[dry-run] Would copy conf.d/isucon-alp-logging.conf -> /etc/nginx/conf.d/"
	echo "[dry-run] Would install $NGINX_DIR/isupipe.conf -> $VHOST_DST (includes listen 443 ssl http2)"
	echo "[dry-run] Would patch /etc/nginx/nginx.conf worker settings per nginx-main-snippet.conf (idempotent markers)"
	echo "[dry-run] Would NOT reload nginx (set CONFIRM_NGINX_RELOAD=yes after nginx -t)"
}

patch_nginx_main_tuning() {
	local main=/etc/nginx/nginx.conf
	local marker_begin="# BEGIN isucon13-provisioning main tuning"

	if [[ "${NGINX_APPLY_DRY_RUN:-}" == "1" ]]; then
		echo "[dry-run] Would ensure main tuning block in $main"
		return 0
	fi

	if [[ ! -f "$main" ]]; then
		echo "Missing $main — install nginx first" >&2
		return 1
	fi

	python3 - "$main" <<'PY'
import sys
path = sys.argv[1]
begin = "# BEGIN isucon13-provisioning main tuning"
end = "# END isucon13-provisioning main tuning"
block = (
    f"{begin}\n"
    "worker_processes auto;\n"
    "worker_rlimit_nofile 65535;\n"
    f"{end}\n"
)
lines = open(path, encoding="utf-8").read().splitlines(keepends=True)
out = []
skip = False
replaced = False
for line in lines:
    if begin in line:
        out.append(block)
        skip = True
        replaced = True
        continue
    if end in line:
        skip = False
        continue
    if not skip:
        out.append(line)
if not replaced:
    if out and out[0].startswith("#"):
        out.insert(1, block)
    else:
        out.insert(0, block)
open(path, "w", encoding="utf-8").writelines(out)
PY

	if grep -q 'worker_connections' "$main"; then
		sed -i 's/^[[:space:]]*worker_connections[[:space:]]\+[0-9]\+;/    worker_connections 4096;/' "$main"
	elif grep -q 'events[[:space:]]*{' "$main"; then
		sed -i '/events[[:space:]]*{/a\    worker_connections 4096;\n    multi_accept on;' "$main"
	fi
}

if [[ "${NGINX_APPLY_DRY_RUN:-}" == "1" ]]; then
	apply_nginx_dry_run
	exit 0
fi

if [[ "${EUID:-$(id -u)}" -ne 0 ]]; then
	echo "Run as root: sudo CONFIRM_NGINX_APPLY=yes $0" >&2
	exit 1
fi

if [[ "${CONFIRM_NGINX_APPLY:-}" != "yes" ]]; then
	echo "Aborted: set CONFIRM_NGINX_APPLY=yes to install nginx configs." >&2
	echo "Preview: NGINX_APPLY_DRY_RUN=1 sudo $0" >&2
	exit 1
fi

install -D -m 0644 "$NGINX_DIR/conf.d/isucon-http-tuning.conf" /etc/nginx/conf.d/isucon-http-tuning.conf
install -D -m 0644 "$NGINX_DIR/conf.d/isucon-alp-logging.conf" /etc/nginx/conf.d/isucon-alp-logging.conf
install -D -m 0644 "$NGINX_DIR/isupipe.conf" "$VHOST_DST"

patch_nginx_main_tuning

echo "Installed nginx conf.d snippets and $VHOST_DST (HTTP/2 vhost from repo)."
echo "Run: sudo nginx -t && sudo CONFIRM_NGINX_RELOAD=yes systemctl reload nginx"
