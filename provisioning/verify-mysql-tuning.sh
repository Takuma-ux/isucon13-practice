#!/usr/bin/env bash
# Read-only check after apply on the target server. Safe on any host (queries MySQL only).
# Confirms pool size matches computed policy (cnf uses InnoDB round-up).
set -euo pipefail

ROOT="$(cd "$(dirname "$0")/.." && pwd)"
# shellcheck disable=SC1091
source "$ROOT/provisioning/lib/mysql-pool-math.sh"

read -r mem_kb policy_raw_mb effective_mb < <(compute_mysql_pool_mb_triplet)
requested_bytes=$((effective_mb * 1024 * 1024))

MYSQL_USER="${ISUCON13_MYSQL_DIALCONFIG_USER:-isucon}"
MYSQL_PASS="${ISUCON13_MYSQL_DIALCONFIG_PASSWORD:-isucon}"
MYSQL_DB="${ISUCON13_MYSQL_DIALCONFIG_DATABASE:-isupipe}"

read -r actual chunk instances < <(
	mysql -u"$MYSQL_USER" -p"$MYSQL_PASS" -h127.0.0.1 -Nse \
		"SELECT @@innodb_buffer_pool_size, @@innodb_buffer_pool_chunk_size, @@innodb_buffer_pool_instances" \
		"$MYSQL_DB"
)

granularity=$((chunk * instances))

echo "MemTotal=${mem_kb}kB policy_raw=${policy_raw_mb}M effective_cnf=${effective_mb}M (${requested_bytes} bytes)"
echo "@@innodb_buffer_pool_chunk_size=${chunk} @@innodb_buffer_pool_instances=${instances} granularity=${granularity} bytes"
echo "@@innodb_buffer_pool_size=${actual} bytes"

if [[ "$actual" -eq "$requested_bytes" ]]; then
	echo "OK: live buffer pool matches effective cnf value (InnoDB round-up applied at provision time)"
else
	echo "MISMATCH: expected ${requested_bytes}, got ${actual}" >&2
	echo "Hint: re-run apply-mysql-tuning on this host or check manual overrides." >&2
	exit 1
fi

if [[ "$effective_mb" -lt "$policy_raw_mb" ]]; then
	echo "WARN: effective < policy_raw (unexpected)" >&2
	exit 1
fi
if [[ "$effective_mb" -gt "$policy_raw_mb" ]]; then
	echo "NOTE: effective ${effective_mb}M > policy_raw ${policy_raw_mb}M (InnoDB chunk alignment — not an apply bug)"
fi

echo "--- InnoDB I/O (read-only) ---"
mysql -u"$MYSQL_USER" -p"$MYSQL_PASS" -h127.0.0.1 -Nse \
	"SELECT @@innodb_flush_neighbors, @@innodb_flush_method, @@innodb_io_capacity, @@innodb_io_capacity_max" \
	"$MYSQL_DB" | awk '{print "innodb_flush_neighbors=" $1 " innodb_flush_method=" $2 " innodb_io_capacity=" $3 " innodb_io_capacity_max=" $4}'

if [[ -f /etc/mysql/mysql.conf.d/isucon-tuning.cnf ]]; then
	echo "--- /etc/mysql/mysql.conf.d/isucon-tuning.cnf ---"
	cat /etc/mysql/mysql.conf.d/isucon-tuning.cnf
fi
