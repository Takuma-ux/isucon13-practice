#!/usr/bin/env bash
# Install repo-maintained infra tuning on an ISUCON13 app host (run with sudo or as root).
set -euo pipefail
ROOT="$(cd "$(dirname "$0")/.." && pwd)"

install -D -m 0644 "$ROOT/provisioning/sysctl/99-isucon.conf" /etc/sysctl.d/99-isucon.conf
sysctl --system

install -D -m 0644 \
  "$ROOT/provisioning/systemd/isupipe-go.service.d/limits.conf" \
  /etc/systemd/system/isupipe-go.service.d/limits.conf
systemctl daemon-reload

echo "Applied sysctl + isupipe-go.service.d/limits.conf"
echo "Optional: merge provisioning/env.sh.example into /home/isucon/env.sh and restart isupipe-go"
echo "Optional: nginx — diff provisioning/nginx/isupipe.conf against /etc/nginx/sites-enabled/"
echo "Optional: mysql — provisioning/mysql/isucon-tuning.cnf then systemctl restart mysql"
