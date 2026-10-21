#!/usr/bin/env bash

set -eux
cd "$(dirname "$0")"

# shellcheck disable=SC1091
source "$(dirname "$0")/mysql_cli.sh"

# MySQLを初期化
mysql_base < init.sql
mysql_base < initial_users.sql
mysql_base < initial_livestreams.sql
mysql_base < initial_tags.sql
mysql_base < initial_livestream_tags.sql
mysql_base < initial_reservation_slots.sql
mysql_base < initial_reactions.sql
mysql_base < initial_ngwords.sql
mysql_base < initial_livecomments.sql

bash ../pdns/init_zone.sh
