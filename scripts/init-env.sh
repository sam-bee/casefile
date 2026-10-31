#!/bin/sh
set -eu

cd "$(dirname "$0")/.."
if [ -e .env ]; then
    echo '.env already exists; leaving it unchanged.'
    exit 0
fi

umask 077
password="$(openssl rand -hex 32)"
data_dir="${XDG_STATE_HOME:-$HOME/.local/state}/nightwatch-casefile"
mkdir -p "$data_dir/postgres" "$data_dir/elasticsearch"
chmod 700 "$data_dir" "$data_dir/postgres" "$data_dir/elasticsearch"
printf 'CASEFILE_DB_PASSWORD=%s\nCASEFILE_PORT=8090\nCASEFILE_DATA_DIR=%s\n' "$password" "$data_dir" > .env
echo 'Created private .env for the local Compose stack.'
