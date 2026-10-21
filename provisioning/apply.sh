#!/usr/bin/env bash
# Install repo-maintained infra tuning on an ISUCON13 app host (run with sudo or as root).
#
# Run ONLY on the competition EC2 instance — not on a dev laptop.
#
# Safety: without confirmation, this script does NOT write /etc, reload services, or
# change running units. Preview: INFRA_DRY_RUN=1 sudo ./provisioning/apply.sh
#
# Activate on target server:
#   sudo CONFIRM_INFRA_APPLY=yes CONFIRM_MYSQL_TUNING=yes ./provisioning/apply.sh
set -euo pipefail
ROOT="$(cd "$(dirname "$0")/.." && pwd)"

if [[ "${INFRA_DRY_RUN:-}" == "1" ]]; then
	infra_dry_run() {
	echo "[dry-run] Would install $ROOT/provisioning/sysctl/99-isucon.conf -> /etc/sysctl.d/"
	echo "[dry-run] Would install 99-isucon-bbr.conf only if tcp_bbr available on host"
	echo "[dry-run] Would install isupipe-go.service.d/limits.conf"
	echo "[dry-run] Would run sysctl --system"
	echo "[dry-run] Would systemctl daemon-reload"
	echo "[dry-run] Would disable apt/unattended-upgrades and noise timers"
	echo "[dry-run] Would run apply_mysql_tuning (needs CONFIRM_MYSQL_TUNING=yes)"
	echo "[dry-run] Nginx: merge files under provisioning/nginx/ manually; reload only with CONFIRM_NGINX_RELOAD=yes"
	}
	infra_dry_run
	# shellcheck disable=SC1091
	source "$(dirname "$0")/apply-mysql-tuning.sh"
	MYSQL_TUNING_DRY_RUN=1 apply_mysql_tuning
	exit 0
fi

if [[ "${EUID:-$(id -u)}" -ne 0 ]]; then
	echo "Run as root: sudo CONFIRM_INFRA_APPLY=yes $0" >&2
	exit 1
fi

if [[ "${CONFIRM_INFRA_APPLY:-}" != "yes" ]]; then
	echo "Aborted: no changes made." >&2
	echo "Set CONFIRM_INFRA_APPLY=yes on the competition EC2 host to install and activate." >&2
	echo "Preview: INFRA_DRY_RUN=1 sudo $0" >&2
	exit 1
fi

install -D -m 0644 "$ROOT/provisioning/sysctl/99-isucon.conf" /etc/sysctl.d/99-isucon.conf

BBR_CONF=/etc/sysctl.d/99-isucon-bbr.conf
if [[ -r /proc/sys/net/ipv4/tcp_available_congestion_control ]] && \
	grep -qw bbr /proc/sys/net/ipv4/tcp_available_congestion_control; then
	install -D -m 0644 "$ROOT/provisioning/sysctl/99-isucon-bbr.conf" "$BBR_CONF"
	echo "BBR: installed $BBR_CONF (tcp_bbr available)"
else
	rm -f "$BBR_CONF"
	echo "BBR: skipped (tcp_bbr not in tcp_available_congestion_control; dev laptop OK)"
fi

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
echo "Optional: merge provisioning/env.sh.example into /home/isucon/env.sh and restart isupipe-go (manual)"
echo "Optional: nginx — see provisioning/README.md; reload only with CONFIRM_NGINX_RELOAD=yes"
