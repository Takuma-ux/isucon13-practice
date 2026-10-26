#!/usr/bin/env bash
# Install repo-maintained infra tuning on an ISUCON13 host (run with sudo or as root).
#
# Run ONLY on the competition EC2 instance — not on a dev laptop.
#
# ISUCON_HOST_ROLE (see provisioning/lib/host-role.sh):
#   app      — srv1/srv2: sysctl, timers, isupipe limits, nginx (via apply-nginx.sh)
#   db       — srv3: sysctl (swappiness 1), MySQL tuning only
#   combined — single VM (default): all of the above
#
# Safety: without confirmation, this script does NOT write /etc, reload services, or
# change running units. Preview: INFRA_DRY_RUN=1 sudo ./provisioning/apply.sh
#
# Activate on target server:
#   sudo CONFIRM_INFRA_APPLY=yes CONFIRM_MYSQL_TUNING=yes ISUCON_HOST_ROLE=app ./provisioning/apply.sh
set -euo pipefail
ROOT="$(cd "$(dirname "$0")/.." && pwd)"
PROV="$ROOT/provisioning"

# shellcheck disable=SC1091
source "$PROV/lib/host-role.sh"
resolve_host_role

install_sysctl_dropins() {
	local role="${ISUCON_HOST_ROLE:-combined}"
	local sysctl_d=/etc/sysctl.d

	install -D -m 0644 "$PROV/sysctl/99-isucon.conf" "$sysctl_d/99-isucon.conf"

	local bbr_conf="$sysctl_d/99-isucon-bbr.conf"
	if [[ -r /proc/sys/net/ipv4/tcp_available_congestion_control ]] && \
		grep -qw bbr /proc/sys/net/ipv4/tcp_available_congestion_control; then
		install -D -m 0644 "$PROV/sysctl/99-isucon-bbr.conf" "$bbr_conf"
		echo "BBR: installed $bbr_conf (tcp_bbr available)"
	else
		rm -f "$bbr_conf"
		echo "BBR: skipped (tcp_bbr not in tcp_available_congestion_control; dev laptop OK)"
	fi

	# Explicit swappiness precedence (do not rely on 99-isucon-db vs 99-isucon lex order).
	case "$role" in
	db)
		install -D -m 0644 "$PROV/sysctl/99-isucon-db.conf" "$sysctl_d/99-isucon-db.conf"
		rm -f "$sysctl_d/100-isucon-swappiness.conf"
		echo "sysctl: db role — vm.swappiness=1 via 99-isucon-db.conf"
		;;
	app | combined)
		rm -f "$sysctl_d/99-isucon-db.conf"
		install -D -m 0644 "$PROV/sysctl/100-isucon-swappiness-app.conf" "$sysctl_d/100-isucon-swappiness.conf"
		echo "sysctl: ${role} role — vm.swappiness=10 via 100-isucon-swappiness.conf"
		;;
	esac
}

if [[ "${INFRA_DRY_RUN:-}" == "1" ]]; then
	echo "[dry-run] ISUCON_HOST_ROLE=${ISUCON_HOST_ROLE:-combined}"
	echo "[dry-run] Would install sysctl drop-ins (role-aware swappiness)"
	echo "[dry-run] Would run sysctl --system"
	if host_role_is_app_surface; then
		echo "[dry-run] Would install isupipe-go.service.d/limits.conf"
		echo "[dry-run] Would systemctl daemon-reload"
		echo "[dry-run] Would disable apt/unattended-upgrades and noise timers"
		echo "[dry-run] Would run apply-nginx.sh when CONFIRM_NGINX_APPLY=yes"
		NGINX_APPLY_DRY_RUN=1 "$PROV/apply-nginx.sh" || true
	else
		echo "[dry-run] Skip isupipe systemd limits, timers, and nginx (db role)"
	fi
	if host_role_runs_mysql_tuning; then
		echo "[dry-run] Would run apply_mysql_tuning (needs CONFIRM_MYSQL_TUNING=yes)"
		# shellcheck disable=SC1091
		source "$PROV/apply-mysql-tuning.sh"
		MYSQL_TUNING_DRY_RUN=1 apply_mysql_tuning
	else
		echo "[dry-run] Skip MySQL tuning (app-only role)"
	fi
	exit 0
fi

if [[ "${EUID:-$(id -u)}" -ne 0 ]]; then
	echo "Run as root: sudo CONFIRM_INFRA_APPLY=yes $0" >&2
	exit 1
fi

if [[ "${CONFIRM_INFRA_APPLY:-}" != "yes" ]]; then
	echo "Aborted: no changes made." >&2
	echo "Set CONFIRM_INFRA_APPLY=yes on the competition EC2 host to install and activate." >&2
	echo "Preview: INFRA_DRY_RUN=1 sudo ISUCON_HOST_ROLE=app $0" >&2
	exit 1
fi

echo "=== ISUCON_HOST_ROLE=${ISUCON_HOST_ROLE:-combined} ==="
install_sysctl_dropins
sysctl --system

if host_role_is_app_surface; then
	install -D -m 0644 \
		"$PROV/systemd/isupipe-go.service.d/limits.conf" \
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

	echo "=== Nginx (identical on every app host) ==="
	if [[ "${CONFIRM_NGINX_APPLY:-}" == "yes" ]]; then
		CONFIRM_NGINX_APPLY=yes "$PROV/apply-nginx.sh"
	else
		echo "Skipped nginx file install (set CONFIRM_NGINX_APPLY=yes to copy vhost + conf.d on this run)."
	fi
else
	echo "=== Skipping app-surface steps (isupipe limits, timers inventory, nginx) on db role ==="
fi

if host_role_runs_mysql_tuning; then
	echo "=== MySQL tuning ==="
	# shellcheck disable=SC1091
	source "$PROV/apply-mysql-tuning.sh"
	apply_mysql_tuning
else
	echo "=== Skipping MySQL tuning on app-only role ==="
fi

echo "=== Done ==="
echo "Applied: role=${ISUCON_HOST_ROLE:-combined} sysctl, optional app/nginx/systemd, optional MySQL"
echo "Nginx reload: sudo nginx -t && CONFIRM_NGINX_RELOAD=yes systemctl reload nginx"
echo "Verify swappiness: sysctl vm.swappiness  (db=1, app/combined=10)"
