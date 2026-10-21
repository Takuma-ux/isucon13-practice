#!/usr/bin/env bash
# Drop legacy ng_words.ng_words_word — unused by any reference implementation.
# Fresh installs: 10_schema.sql no longer creates this index.
#
# Usage (existing DB only, after review):
#   CONFIRM_DROP_NG_WORDS_WORD=yes ./scripts/drop-ng_words-word-index.sh
#   ./scripts/drop-ng_words-word-index.sh --check   # exit 1 if index still present
set -euo pipefail

ROOT="$(cd "$(dirname "$0")/.." && pwd)"
# shellcheck disable=SC1091
source "${ROOT}/sql/mysql_cli.sh"

MODE=apply
if [[ "${1:-}" == "--check" ]]; then
	MODE=check
elif [[ -n "${1:-}" ]]; then
	echo "usage: CONFIRM_DROP_NG_WORDS_WORD=yes $0" >&2
	echo "       $0 --check" >&2
	exit 2
fi

if ! mysql_base -e "SELECT 1" >/dev/null 2>&1; then
	echo "MySQL に接続できません" >&2
	exit 1
fi

exists="$(mysql_base -Nse "
	SELECT 1 FROM information_schema.statistics
	WHERE table_schema = DATABASE()
	  AND table_name = 'ng_words'
	  AND index_name = 'ng_words_word'
	LIMIT 1
" 2>/dev/null || true)"

if [[ "$exists" != "1" ]]; then
	echo "OK: ng_words.ng_words_word は存在しません"
	exit 0
fi

if [[ "$MODE" == "check" ]]; then
	echo "PRESENT: ng_words.ng_words_word — drop with CONFIRM_DROP_NG_WORDS_WORD=yes $0" >&2
	exit 1
fi

if [[ "${CONFIRM_DROP_NG_WORDS_WORD:-}" != "yes" ]]; then
	echo "Aborted: set CONFIRM_DROP_NG_WORDS_WORD=yes to DROP INDEX ng_words_word on ng_words." >&2
	exit 1
fi

echo "ALTER TABLE ng_words DROP INDEX ng_words_word;"
mysql_base -e "ALTER TABLE \`ng_words\` DROP INDEX \`ng_words_word\`;"
echo "Dropped ng_words.ng_words_word."
