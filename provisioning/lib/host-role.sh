# shellcheck shell=bash
# ISUCON13 provisioning host role (sourced by apply.sh / apply-nginx.sh).
#
# ISUCON_HOST_ROLE:
#   app      — nginx + isupipe + sysctl (swappiness 10); no MySQL tuning
#   db       — MySQL tuning + DB sysctl (swappiness 1); no nginx / isupipe limits
#   combined — single-node stock layout (default): all of the above
#
# Lexicographic sysctl.d order is NOT relied on for swappiness; apply.sh installs
# role-specific drop-ins explicitly (see provisioning/README.md).

resolve_host_role() {
	case "${ISUCON_HOST_ROLE:-combined}" in
	app | db | combined) ;;
	*)
		echo "Invalid ISUCON_HOST_ROLE=${ISUCON_HOST_ROLE:-} (use app, db, or combined)" >&2
		exit 1
		;;
	esac
}

host_role_is_app_surface() {
	[[ "${ISUCON_HOST_ROLE:-combined}" == "app" || "${ISUCON_HOST_ROLE:-combined}" == "combined" ]]
}

host_role_runs_mysql_tuning() {
	[[ "${ISUCON_HOST_ROLE:-combined}" == "db" || "${ISUCON_HOST_ROLE:-combined}" == "combined" ]]
}
