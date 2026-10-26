# shellcheck shell=bash
# Shared mysql client flags for init.sh and other tooling.
# Matches go connectDB() env vars (ISUCON13_MYSQL_DIALCONFIG_*).

if test -f /home/isucon/env.sh; then
	# shellcheck disable=SC1091
	. /home/isucon/env.sh
fi

ISUCON_DB_NET=${ISUCON13_MYSQL_DIALCONFIG_NET:-tcp}
ISUCON_DB_HOST=${ISUCON13_MYSQL_DIALCONFIG_ADDRESS:-127.0.0.1}
ISUCON_DB_PORT=${ISUCON13_MYSQL_DIALCONFIG_PORT:-3306}
ISUCON_DB_USER=${ISUCON13_MYSQL_DIALCONFIG_USER:-isucon}
ISUCON_DB_PASSWORD=${ISUCON13_MYSQL_DIALCONFIG_PASSWORD:-isucon}
ISUCON_DB_NAME=${ISUCON13_MYSQL_DIALCONFIG_DATABASE:-isupipe}

mysql_base() {
	local args=(-u"$ISUCON_DB_USER" -p"$ISUCON_DB_PASSWORD")
	if [[ "$ISUCON_DB_NET" == "unix" ]]; then
		args+=(--socket="$ISUCON_DB_HOST")
	else
		args+=(--host "$ISUCON_DB_HOST" --port "$ISUCON_DB_PORT")
	fi
	mysql "${args[@]}" "$ISUCON_DB_NAME" "$@"
}
