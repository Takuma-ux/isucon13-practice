#!/usr/bin/env bash
# Read-only check after apply on the target server. Safe on any host (queries MySQL only).
# Confirms pool size matches computed policy (including InnoDB round-up to
# chunk_size × instances).
set -euo pipefail

mem_kb="$(awk '/^MemTotal:/ {print $2; exit}' /proc/meminfo)"
pool_mb="$((mem_kb * 45 / 100 / 1024))"
pool_mb="$((pool_mb / 128 * 128))"
if [[ "$pool_mb" -lt 128 ]]; then
	pool_mb=128
fi
requested_bytes=$((pool_mb * 1024 * 1024))

MYSQL_USER="${ISUCON13_MYSQL_DIALCONFIG_USER:-isucon}"
MYSQL_PASS="${ISUCON13_MYSQL_DIALCONFIG_PASSWORD:-isucon}"
MYSQL_DB="${ISUCON13_MYSQL_DIALCONFIG_DATABASE:-isupipe}"

read -r actual chunk instances < <(
	mysql -u"$MYSQL_USER" -p"$MYSQL_PASS" -h127.0.0.1 -Nse \
		"SELECT @@innodb_buffer_pool_size, @@innodb_buffer_pool_chunk_size, @@innodb_buffer_pool_instances" \
		"$MYSQL_DB"
)

granularity=$((chunk * instances))
# InnoDB rounds requested size up to a multiple of (chunk_size * instances).
effective_bytes=$(( (requested_bytes + granularity - 1) / granularity * granularity ))

echo "MemTotal=${mem_kb}kB policy_requested=${pool_mb}M (${requested_bytes} bytes)"
echo "@@innodb_buffer_pool_chunk_size=${chunk} @@innodb_buffer_pool_instances=${instances} granularity=${granularity} bytes"
echo "effective_expected=${effective_bytes} bytes (after InnoDB round-up)"
echo "@@innodb_buffer_pool_size=${actual} bytes"

if [[ "$actual" -eq "$effective_bytes" ]]; then
	echo "OK: live buffer pool matches policy after InnoDB rounding"
elif [[ "$actual" -ge "$requested_bytes" && "$actual" -lt "$((requested_bytes + granularity))" ]]; then
	echo "OK: live buffer pool within one rounding increment of requested size"
else
	echo "MISMATCH: expected ~${effective_bytes}, got ${actual}" >&2
	exit 1
fi

if [[ -f /etc/mysql/mysql.conf.d/isucon-tuning.cnf ]]; then
	echo "--- /etc/mysql/mysql.conf.d/isucon-tuning.cnf ---"
	cat /etc/mysql/mysql.conf.d/isucon-tuning.cnf
fi
