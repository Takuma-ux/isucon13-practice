# shellcheck shell=bash
# InnoDB buffer pool size policy (sourced by apply-mysql-tuning.sh and verify-mysql-tuning.sh).
#
# MySQL 8 rounds innodb_buffer_pool_size to a multiple of
#   innodb_buffer_pool_chunk_size (default 128M) × innodb_buffer_pool_instances
# (8 when requested pool >= 1G, else 1). Values that are not an exact multiple
# are rounded *down* at runtime — e.g. 2560M configured → 2048M live.
# We round the written cnf value *up* so @@innodb_buffer_pool_size matches the file.

readonly ISUCON_INNODB_CHUNK_MB=128

innodb_buffer_pool_instances_for_mb() {
	local pool_mb="$1"
	if [[ "$pool_mb" -lt 1024 ]]; then
		echo 1
	else
		echo 8
	fi
}

# Round pool_mb up to InnoDB's allocation granularity (chunk × instances).
round_innodb_buffer_pool_mb_up() {
	local pool_mb="$1"
	local instances gran_mb
	instances="$(innodb_buffer_pool_instances_for_mb "$pool_mb")"
	gran_mb=$((ISUCON_INNODB_CHUNK_MB * instances))
	echo $(((pool_mb + gran_mb - 1) / gran_mb * gran_mb))
}

# Prints: mem_kb policy_raw_mb effective_mb
compute_mysql_pool_mb_triplet() {
	local mem_kb policy_raw_mb effective_mb
	mem_kb="$(awk '/^MemTotal:/ {print $2; exit}' /proc/meminfo)"
	# ~45% of RAM; round down to 128MiB steps (policy target before InnoDB alignment).
	policy_raw_mb="$((mem_kb * 45 / 100 / 1024))"
	policy_raw_mb="$((policy_raw_mb / 128 * 128))"
	if [[ "$policy_raw_mb" -lt 128 ]]; then
		policy_raw_mb=128
	fi
	effective_mb="$(round_innodb_buffer_pool_mb_up "$policy_raw_mb")"
	echo "$mem_kb $policy_raw_mb $effective_mb"
}
