#!/usr/bin/env bash
# isupipe の想定インデックスを information_schema と照合し、不足分だけ ADD INDEX する。
# 用法:
#   ./scripts/ensure_isupipe_indexes.sh --check   # 不足があれば exit 1
#   ./scripts/ensure_isupipe_indexes.sh --apply   # 不足を追加（既定）
set -euo pipefail

ROOT="$(cd "$(dirname "$0")/.." && pwd)"
MANIFEST="${ROOT}/sql/indexes/manifest.tsv"

if test -f /home/isucon/env.sh; then
	# shellcheck disable=SC1091
	. /home/isucon/env.sh
fi

ISUCON_DB_HOST=${ISUCON13_MYSQL_DIALCONFIG_ADDRESS:-127.0.0.1}
ISUCON_DB_PORT=${ISUCON13_MYSQL_DIALCONFIG_PORT:-3306}
ISUCON_DB_USER=${ISUCON13_MYSQL_DIALCONFIG_USER:-isucon}
ISUCON_DB_PASSWORD=${ISUCON13_MYSQL_DIALCONFIG_PASSWORD:-isucon}
ISUCON_DB_NAME=${ISUCON13_MYSQL_DIALCONFIG_DATABASE:-isupipe}

MODE=apply
if [[ "${1:-}" == "--check" ]]; then
	MODE=check
elif [[ "${1:-}" == "--apply" || -z "${1:-}" ]]; then
	MODE=apply
elif [[ -n "${1:-}" ]]; then
	echo "usage: $0 [--check|--apply]" >&2
	exit 2
fi

mysql_base() {
	mysql -u"$ISUCON_DB_USER" -p"$ISUCON_DB_PASSWORD" \
		--host "$ISUCON_DB_HOST" \
		--port "$ISUCON_DB_PORT" \
		"$ISUCON_DB_NAME" "$@"
}

if ! mysql_base -e "SELECT 1" >/dev/null 2>&1; then
	echo "MySQL に接続できません (${ISUCON_DB_USER}@${ISUCON_DB_HOST}:${ISUCON_DB_PORT}/${ISUCON_DB_NAME})" >&2
	exit 1
fi

index_exists() {
	local table="$1" index_name="$2"
	local found
	found="$(mysql_base -Nse "
		SELECT 1 FROM information_schema.statistics
		WHERE table_schema = DATABASE()
		  AND table_name = '${table}'
		  AND index_name = '${index_name}'
		LIMIT 1
	" 2>/dev/null || true)"
	[[ "$found" == "1" ]]
}

missing=0
while IFS=$'\t' read -r table index_name columns _rest; do
	[[ -z "${table:-}" || "${table:0:1}" == "#" ]] && continue
	if index_exists "$table" "$index_name"; then
		echo "OK  ${table}.${index_name} (${columns})"
	else
		echo "MISSING  ${table}.${index_name} (${columns})"
		missing=1
		if [[ "$MODE" == "apply" ]]; then
			echo "  -> ALTER TABLE \`${table}\` ADD INDEX \`${index_name}\` (${columns//,/, });"
			mysql_base -e "ALTER TABLE \`${table}\` ADD INDEX \`${index_name}\` (${columns//,/, });"
			echo "  -> added"
		fi
	fi
done < "$MANIFEST"

if [[ "$missing" -eq 1 && "$MODE" == "check" ]]; then
	echo "一部インデックスが不足しています。本番では: $0 --apply" >&2
	exit 1
fi

if [[ "$missing" -eq 0 ]]; then
	echo "すべての想定インデックスが存在します。"
fi
