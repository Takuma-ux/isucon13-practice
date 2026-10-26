#!/usr/bin/env bash
# Capture CPU profile from a RUNNING isupipe (pprof on :6060), replace go/default.pgo,
# update default.pgo.meta, and rebuild the binary in one step.
#
# Intended flow: finish (or tail) a real ./bench run → run this script immediately →
# next deploy/build uses the freshest traffic shape via -pgoprofile=default.pgo.
#
# Usage (from repo root or anywhere):
#   ./go/scripts/rebuild-pgo-from-profile.sh
#
# Environment:
#   PGO_PPROF_URL         Base URL for pprof (default http://127.0.0.1:6060)
#   PGO_PROFILE_SECONDS   Profile wall time (default 30)
#   PGO_CAPTURE_SOURCE    Meta label: e.g. "real bench run" (default)
#   PGO_BENCH_TARGET      Optional bench URL/host note for meta
#   PGO_RAW_PROF          Where to store raw .prof (default ${TMPDIR}/isupipe-pgo-bench.prof)
#   GO_BUILD_OUTPUT       Binary path relative to go/ (default ../isupipe)
#   GOAMD64               Passed through to go build (optional, see BUILD.md)
set -euo pipefail

ROOT="$(cd "$(dirname "$0")/../.." && pwd)"
GO_DIR="$ROOT/go"
PPROF_BASE="${PGO_PPROF_URL:-http://127.0.0.1:6060}"
PROFILE_SEC="${PGO_PROFILE_SECONDS:-30}"
CAPTURE_SOURCE="${PGO_CAPTURE_SOURCE:-real bench run}"
BENCH_TARGET="${PGO_BENCH_TARGET:-}"
PPROF_OUT="${PGO_RAW_PROF:-${TMPDIR:-/tmp}/isupipe-pgo-bench.prof}"
BUILD_OUT="${GO_BUILD_OUTPUT:-../isupipe}"
PGO_FILE="$GO_DIR/default.pgo"
META_FILE="$GO_DIR/default.pgo.meta"

if ! command -v curl >/dev/null; then
	echo "curl required" >&2
	exit 1
fi

echo "Checking pprof at ${PPROF_BASE} ..."
if ! curl -sf --max-time 3 "${PPROF_BASE}/debug/pprof/" >/dev/null; then
	echo "pprof not reachable at ${PPROF_BASE} — is isupipe running with net/http/pprof on :6060?" >&2
	exit 1
fi

echo "Capturing ${PROFILE_SEC}s CPU profile ..."
curl -sS -o "$PPROF_OUT" "${PPROF_BASE}/debug/pprof/profile?seconds=${PROFILE_SEC}"

if [[ ! -s "$PPROF_OUT" ]]; then
	echo "Empty profile at $PPROF_OUT" >&2
	exit 1
fi

if [[ -f "$PGO_FILE" ]]; then
	cp -a "$PGO_FILE" "${PGO_FILE}.bak.$(date -u +%Y%m%dT%H%M%SZ)"
fi

echo "Writing $PGO_FILE ..."
go tool pprof -proto -output="$PGO_FILE" "$PPROF_OUT"

GENERATED_UTC="$(date -u +%Y-%m-%dT%H:%M:%SZ)"
HOSTNAME_SHORT="$(hostname -s 2>/dev/null || hostname)"
cat >"$META_FILE" <<EOF
# Metadata for default.pgo (NOT used by the Go compiler — for humans only)
#
# STATUS: ${CAPTURE_SOURCE}
#
# Generated: ${GENERATED_UTC} via go/scripts/rebuild-pgo-from-profile.sh
# Host:       ${HOSTNAME_SHORT}
# Traffic:    ${CAPTURE_SOURCE}
# Profile:    ${PROFILE_SEC}s CPU from ${PPROF_BASE}/debug/pprof/profile
# Source prof: ${PPROF_OUT}
EOF
if [[ -n "$BENCH_TARGET" ]]; then
	echo "# Bench target: ${BENCH_TARGET}" >>"$META_FILE"
fi
cat >>"$META_FILE" <<EOF
#
# Next build: cd go && go build -o ${BUILD_OUT} .  (uses -pgoprofile=default.pgo automatically)
EOF

echo "Rebuilding binary (go build -o ${BUILD_OUT}) ..."
BUILD_ENV=()
if [[ -n "${GOAMD64:-}" ]]; then
	BUILD_ENV=(env GOAMD64="$GOAMD64")
fi
(
	cd "$GO_DIR"
	"${BUILD_ENV[@]}" go build -o "$BUILD_OUT" .
)

echo "Verifying PGO wired into compile ..."
VERIFY_LOG="$(mktemp)"
trap 'rm -f "$VERIFY_LOG"' EXIT
( cd "$GO_DIR" && go build -a -x -o /dev/null . ) >"$VERIFY_LOG" 2>&1
if grep -qi pgoprofile "$VERIFY_LOG"; then
	echo "OK: go build uses -pgoprofile=.../default.pgo"
else
	echo "WARN: pgoprofile not seen in go build -a -x output (missing default.pgo?)" >&2
	exit 1
fi

BUILT_PATH="$BUILD_OUT"
if [[ "$BUILT_PATH" != /* ]]; then
	BUILT_PATH="$GO_DIR/$BUILD_OUT"
fi
ls -la "$PGO_FILE" "$META_FILE" "$BUILT_PATH"
echo "Done. Restart app to serve the PGO-optimized binary (e.g. systemctl restart isupipe-go)."
