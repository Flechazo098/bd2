#!/bin/sh
set -eu

data_dir=/app/data
update_config=
next=
for argument in "$@"; do
    if [ -n "$next" ]; then
        case "$next" in
            data) data_dir=$argument ;;
            update) update_config=$argument ;;
        esac
        next=
        continue
    fi
    case "$argument" in
        --data-dir|-data-dir) next=data ;;
        --data-dir=*|-data-dir=*) data_dir=${argument#*=} ;;
        --update-config|-update-config) next=update ;;
        --update-config=*|-update-config=*) update_config=${argument#*=} ;;
    esac
done
[ -z "$next" ] || { printf '%s\n' 'Missing configuration path argument' >&2; exit 2; }
if [ -z "$update_config" ]; then
    update_config=$data_dir/config/updates.json
    if [ ! -e "$update_config" ]; then
        mkdir -p "$(dirname "$update_config")"
        (umask 077; set -C; printf '%s\n' '{"method":"docker"}' >"$update_config")
    fi
fi
exec /app/bd2server "$@"
