#!/usr/bin/env bash
# Single entry point for competition-day deploy: ordered checklist + optional preflight/run helpers.
#
#   ./provisioning/deploy-checklist.sh              # print full ordered checklist (default)
#   ./provisioning/deploy-checklist.sh --preflight  # safe local checks (build, index --check, mysql verify)
#   ./provisioning/deploy-checklist.sh --dry-run    # preview infra/mysql/slow-log without applying
#
# Destructive steps still require their own CONFIRM_* flags (never auto-enabled here).
set -euo pipefail

ROOT="$(cd "$(dirname "$0")/.." && pwd)"
cd "$ROOT"

print_checklist() {
	cat <<EOF
================================================================================
 ISUCON13 deploy checklist (competition EC2 app server)
 Repo: $ROOT
================================================================================

── 1. Code on server ──
  [ ] git pull (or deploy branch)
      cd $ROOT && git pull

── 2. Build app ──
  [ ] Optional PGO: capture default.pgo during bench (see go/PGO.md); file must live in go/
  [ ] Safe CPU target (unknown instance type): GOAMD64=v2 go build -o ../isupipe ./
      (see go/BUILD.md — use v3 only after confirming avx2 on the server)
  [ ] cd go && go build -o ../isupipe ./   # or your AMI's build path / systemd unit

── 3. MySQL indexes ──
  [ ] Check:  ./scripts/ensure_isupipe_indexes.sh --check
  [ ] Apply:  ./scripts/ensure_isupipe_indexes.sh --apply
  [ ] Optional (existing DB only): ./scripts/drop-ng_words-word-index.sh --check
      then CONFIRM_DROP_NG_WORDS_WORD=yes ./scripts/drop-ng_words-word-index.sh
      (fresh init from 10_schema.sql already omits ng_words_word)
      (uses sql/mysql_cli.sh — set ISUCON_DB_* or env.sh unix socket vars)

── 4. App environment (/home/isucon/env.sh) ──
  [ ] Merge provisioning/env.sh.example (unix socket, PowerDNS address, optional ISUCON13_GZIP)
      diff provisioning/env.sh.example /home/isucon/env.sh
  [ ] Restart app after env change:
      sudo systemctl restart isupipe-go

── 5. Host infra (sysctl, timers, systemd limits) ──
  [ ] Preview:  sudo INFRA_DRY_RUN=1 ./provisioning/apply.sh
  [ ] Apply:    sudo CONFIRM_INFRA_APPLY=yes CONFIRM_MYSQL_TUNING=yes ./provisioning/apply.sh
      (MySQL tuning needs CONFIRM_MYSQL_TUNING=yes — sizes buffer pool from THIS host's RAM)

── 6. MySQL tuning only (if step 5 skipped MySQL) ──
  [ ] Preview:  sudo MYSQL_TUNING_DRY_RUN=1 ./provisioning/apply-mysql-tuning.sh
  [ ] Apply:    sudo CONFIRM_MYSQL_TUNING=yes ./provisioning/apply-mysql-tuning.sh
  [ ] Verify:   ./provisioning/verify-mysql-tuning.sh

── 7. Nginx (manual merge — not auto-copied) ──
  [ ] Merge provisioning/nginx/nginx-main-snippet.conf into /etc/nginx/nginx.conf
  [ ] Copy provisioning/nginx/conf.d/isucon-http-tuning.conf -> /etc/nginx/conf.d/
  [ ] Copy provisioning/nginx/conf.d/isucon-alp-logging.conf -> /etc/nginx/conf.d/
  [ ] Merge provisioning/nginx/isupipe.conf -> sites-enabled (vhost)
  [ ] Test:     sudo nginx -t
  [ ] HTTP/2:   listen 443 ssl http2 (or nginx>=1.25.1: listen 443 ssl + http2 on;)
  [ ] Reload:   sudo CONFIRM_NGINX_RELOAD=yes systemctl reload nginx

── 8. Diagnostic bench run ONLY (turn OFF after) ──
  MySQL slow log:
  [ ] On:   sudo CONFIRM_SLOW_LOG=yes ./provisioning/apply-mysql-slow-log.sh on
  [ ] Bench, then: sudo pt-query-digest /var/log/mysql/isucon-slow.log
  [ ] Off:  sudo CONFIRM_SLOW_LOG=yes ./provisioning/apply-mysql-slow-log.sh off

  Nginx ALP access log:
  [ ] In vhost: use provisioning/nginx/isupipe.access-log-snippet.conf instead of access_log off;
  [ ] nginx -t && reload; run bench; alp json --file=/var/log/nginx/isucon.access.ltsv
  [ ] Restore access_log off; reload

── 9. Initialize & bench ──
  [ ] POST /api/initialize (bench does this)
  [ ] Run bench / verify score

── Quick reference ──
  provisioning/README.md
  Never run CONFIRM_MYSQL_TUNING on a dev laptop (RAM-sized buffer pool).

================================================================================
EOF
}

run_preflight() {
	echo "=== Preflight (no privileged apply) ==="
	echo "--- go build ---"
	( cd go && go build -o /dev/null . )
	echo "OK: go build"

	echo "--- index check ---"
	if ./scripts/ensure_isupipe_indexes.sh --check; then
		echo "OK: indexes"
	else
		echo "WARN: missing indexes — run ensure_isupipe_indexes.sh --apply on server"
	fi

	if [[ -x ./provisioning/verify-mysql-tuning.sh ]]; then
		echo "--- verify-mysql-tuning (read-only if MySQL up) ---"
		./provisioning/verify-mysql-tuning.sh || echo "SKIP/WARN: verify-mysql-tuning failed (MySQL down or no drop-in yet)"
	fi
	echo "=== Preflight done ==="
}

run_dry_run() {
	echo "=== Infra dry-run ==="
	sudo INFRA_DRY_RUN=1 ./provisioning/apply.sh || true
	echo ""
	echo "=== MySQL slow log dry-run (on) ==="
	sudo MYSQL_SLOW_LOG_DRY_RUN=1 ./provisioning/apply-mysql-slow-log.sh on || true
}

case "${1:-}" in
"" | --checklist | --list)
	print_checklist
	;;
--preflight)
	run_preflight
	;;
--dry-run)
	run_dry_run
	;;
-h | --help)
	echo "usage: $0 [--preflight|--dry-run|--help]"
	exit 0
	;;
*)
	echo "Unknown option: $1 (try --help)" >&2
	exit 2
	;;
esac
