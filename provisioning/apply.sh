#!/usr/bin/env bash
# Install repo-maintained infra tuning on an ISUCON13 app host (run with sudo or as root).
#
# Run ONLY on the competition EC2 instance — not on a dev laptop. MySQL buffer pool
# sizing uses this host's RAM (see apply-mysql-tuning.sh); wrong-host runs are dangerous.
# Example: sudo CONFIRM_MYSQL_TUNING=yes ./provisioning/apply.sh
set -euo pipefail
ROOT="$(cd "$(dirname "$0")/.." && pwd)"

if [[ "${EUID:-$(id -u)}" -ne 0 ]]; then
	echo "Run as root: sudo $0" >&2
	exit 1
fi

install -D -m 0644 "$ROOT/provisioning/sysctl/99-isucon.conf" /etc/sysctl.d/99-isucon.conf
sysctl --system

install -D -m 0644 \
	"$ROOT/provisioning/systemd/isupipe-go.service.d/limits.conf" \
	/etc/systemd/system/isupipe-go.service.d/limits.conf
systemctl daemon-reload

disable_unit() {
	local u="$1"
	if systemctl list-unit-files "$u" &>/dev/null; then
		systemctl disable --now "$u" 2>/dev/null || systemctl disable "$u" 2>/dev/null || true
		echo "disabled $u"
	else
		echo "skip (no unit) $u"
	fi
}

echo "=== Disabling automatic package maintenance (Ubuntu 22.04) ==="
disable_unit unattended-upgrades.service
disable_unit apt-daily.timer
disable_unit apt-daily-upgrade.timer

echo "=== Disabling other benchmark-noise timers (safe for competition day) ==="
for t in \
	motd-news.timer \
	man-db.timer \
	plocate-updatedb.timer \
	fwupd-refresh.timer \
	anacron.timer \
	logrotate.timer; do
	disable_unit "$t"
done

echo "=== Scheduled tasks inventory (not modified unless listed above) ==="
echo "-- system crontab (/etc/crontab) --"
if [[ -f /etc/crontab ]]; then
	grep -v '^\s*#' /etc/crontab | grep -v '^\s*$' || true
else
	echo "(missing)"
fi
echo "-- /etc/cron.d --"
ls -la /etc/cron.d 2>/dev/null || true
echo "-- isucon user crontab --"
crontab -l -u isucon 2>/dev/null || echo "(none)"
echo "-- active timers (after disable) --"
systemctl list-timers --all 2>/dev/null | head -20 || true

echo "=== MySQL tuning (shared host: app + MySQL + PowerDNS on one VM) ==="
# shellcheck disable=SC1091
source "$(dirname "$0")/apply-mysql-tuning.sh"
apply_mysql_tuning

echo "=== Done ==="
echo "Applied: sysctl, isupipe-go limits, apt/unattended-upgrades off, optional timers off, MySQL drop-in"
echo "Optional: merge provisioning/env.sh.example into /home/isucon/env.sh and restart isupipe-go"
echo "Optional: nginx — merge provisioning/nginx/isupipe.conf into /etc/nginx/sites-enabled/ then nginx -t && systemctl reload nginx"
