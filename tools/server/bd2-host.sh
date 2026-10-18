#!/bin/sh
# bd2-host.sh: everything a Docker host does for bd2server, in one place.
#
#   setup [SOURCE]     first install and deploy local source (safe to rerun)
#   setup --image REF  first install and deploy a Docker image
#   install [SOURCE]   prepare directories, secrets, a Compose template and the
#                      update-handoff units; never replaces existing data
#   deploy [SOURCE]    build a source directory (default: /opt/bd2/source),
#                      switch the container and wait until ready; no Git needed
#   deploy --image REF deploy an existing local image or pull it if missing
#   handoff            serve a server-requested update (run by the .path unit)
#   rollback           start the image that ran before the last switch
#   status             show the running image and optional commit
#
# Run as root. Settings come from /etc/bd2/host.conf (KEY=value lines, see
# load_config) and default to a self-contained layout below /opt/bd2.
set -eu
umask 077

CONFIG=/etc/bd2/host.conf

load_config() {
    BD2_DIR=/opt/bd2
    SOURCE_URL=https://git.sighs.cc/Flechazo/bd2.git
    COMPOSE_FILE=
    SERVICE=server
    # The readiness port inside the container; probed through its network
    # namespace, so it does not depend on published host ports.
    READY_PORT=8080
    # Optional directory with authentication.json/resources.json to seed once.
    SEED_CONFIG_DIR=
    if [ -f "$CONFIG" ]; then
        [ "$(stat -c '%u' "$CONFIG")" = 0 ] || die "$CONFIG must be owned by root"
        case "$(stat -c '%a' "$CONFIG")" in *[2367]?|*?[2367]) die "$CONFIG must not be group or world writable" ;; esac
        # shellcheck disable=SC2034 # value is assigned through eval below
        while IFS='=' read -r key value; do
            case "$key" in
                ''|'#'*) ;;
                BD2_DIR|SOURCE_URL|COMPOSE_FILE|SERVICE|READY_PORT|SEED_CONFIG_DIR) eval "$key=\$value" ;;
                *) die "unknown setting in $CONFIG: $key" ;;
            esac
        done <"$CONFIG"
    fi
    case "$READY_PORT" in ''|*[!0-9]*) die "READY_PORT must be a number" ;; esac
    SOURCE_DIR=$BD2_DIR/source
    DATA_DIR=$BD2_DIR/data
    ENV_FILE=$BD2_DIR/.env
    PREVIOUS_FILE=$BD2_DIR/previous-image
    [ -n "$COMPOSE_FILE" ] || COMPOSE_FILE=$BD2_DIR/compose.yaml
    UPDATES=$DATA_DIR/updates
    SELF=$BD2_DIR/bd2-host.sh
}

log() { printf 'bd2-host: %s\n' "$*"; }
die() { log "$*" >&2; exit 1; }

require() {
    for dependency in "$@"; do
        command -v "$dependency" >/dev/null || die "$dependency is required"
    done
}

compose() { docker compose -f "$COMPOSE_FILE" --env-file "$ENV_FILE" "$@"; }

# lock serialises deploys, rollbacks and handoffs on one host.
lock() {
    install -d -m 700 -o 10001 -g 10001 "$UPDATES"
    # Keep the inode: deleting a held lock lets another invocation bypass it.
    exec 9>>"$UPDATES/.docker-update.lock"
    if [ "${1:-wait}" = try ]; then
        flock -n 9 || exit 0
    else
        flock 9
    fi
}

# --- source -------------------------------------------------------------------

verify_source() {
    if [ ! -e "$SOURCE_DIR" ]; then
        git clone --quiet "$SOURCE_URL" "$SOURCE_DIR" || die 'cannot clone the release source'
    fi
    [ -d "$SOURCE_DIR/.git" ] || die "no Git checkout at $SOURCE_DIR for source-based automatic updates"
    actual=$(git -C "$SOURCE_DIR" remote get-url --all origin) || die 'cannot read the source origin'
    actual=${actual%/}
    expected=${SOURCE_URL%/}
    [ "${actual%.git}" = "${expected%.git}" ] || die "source origin $actual is not $SOURCE_URL"
    git -C "$SOURCE_DIR" fetch --quiet --no-tags --prune origin '+refs/heads/*:refs/bd2-update/*' || die 'cannot fetch the source origin'
}

# Automatic updates resolve the commit from the authenticated release request.
resolve_commit() {
    printf '%s\n' "$1" | grep -Eq '^[0-9a-f]{40}$' || die 'COMMIT must be a full 40-digit commit hash'
    [ "$(git -C "$SOURCE_DIR" rev-parse --verify "$1^{commit}" 2>/dev/null)" = "$1" ] || die "$1 is not a commit in the source repository"
    printf '%s\n' "$1"
}

build_source_image() {
    source_path=$(realpath "$1") || die 'cannot resolve the source directory'
    [ -f "$source_path/Dockerfile.server" ] || die "no Dockerfile.server in $source_path"
    docker build --quiet -f "$source_path/Dockerfile.server" "$source_path"
}

resolve_image() {
    docker image inspect --format '{{.Id}}' "$1" 2>/dev/null && return
    docker pull "$1" >&2 || return 1
    docker image inspect --format '{{.Id}}' "$1"
}

# build_image COMMIT prints the immutable image ID built from a fresh
# worktree, so ignored local files never enter the build context.
build_image() {
    build_dir=$(mktemp -d) || die 'cannot create a build directory'
    # COPY keeps file modes and the image runs as an unprivileged user, so
    # check out with a normal umask instead of this script's private 077.
    if ! (umask 022 && git -C "$SOURCE_DIR" -c core.hooksPath=/dev/null worktree add --quiet --detach "$build_dir/source" "$1"); then
        rmdir "$build_dir"
        die "cannot check out $1"
    fi
    built=$(docker build --quiet --label "org.opencontainers.image.revision=$1" -t "bd2server:$1" \
        -f "$build_dir/source/Dockerfile.server" "$build_dir/source") || built=
    git -C "$SOURCE_DIR" worktree remove --force "$build_dir/source" >/dev/null 2>&1 || true
    rmdir "$build_dir" 2>/dev/null || true
    printf '%s\n' "$built" | grep -Eq '^sha256:[0-9a-f]{64}$' || die "building $1 failed"
    printf '%s\n' "$built"
}

# --- container switch ---------------------------------------------------------

# current_container prints the one existing container of the service.
current_container() {
    id=$(compose ps -a -q "$SERVICE") || die 'cannot list the Compose service'
    case "$id" in ''|*[!0-9a-f]*) return 1 ;; esac
    printf '%s\n' "$id"
}

# use_image IMAGE makes .env select IMAGE and recreates the container. .env
# also holds secrets: it is never sourced or printed, and keeps its metadata.
use_image() {
    [ -f "$ENV_FILE" ] && [ ! -L "$ENV_FILE" ] || die "$ENV_FILE must be a regular file"
    metadata=$(stat -c '%u:%g:%a' "$ENV_FILE")
    env_tmp=$(mktemp "$ENV_FILE.update.XXXXXX") || return 1
    if { grep -v '^BD2_SERVER_IMAGE=' "$ENV_FILE" || [ "$?" -eq 1 ]; } >"$env_tmp" &&
        printf 'BD2_SERVER_IMAGE=%s\n' "$1" >>"$env_tmp" &&
        cp --attributes-only --preserve=mode,ownership -- "$ENV_FILE" "$env_tmp" &&
        [ "$(stat -c '%u:%g:%a' "$env_tmp")" = "$metadata" ] &&
        mv -f "$env_tmp" "$ENV_FILE"; then
        compose up -d --no-deps --force-recreate --pull never "$SERVICE"
        return
    fi
    rm -f "$env_tmp"
    return 1
}

# ready IMAGE REPLACED [FINGERPRINT] waits until a new container (not
# REPLACED) runs IMAGE and answers /readyz inside its own network namespace,
# and, when given, reports the expected update fingerprint.
ready() {
    url=http://127.0.0.1:$READY_PORT
    attempts=0
    while [ "$attempts" -lt 60 ]; do
        candidate=$(compose ps -q "$SERVICE") || candidate=
        case "$candidate" in ''|*[!0-9a-f]*) candidate= ;; esac
        if [ -n "$candidate" ] && [ "$candidate" != "$2" ]; then
            identity=$(docker inspect --format '{{.Image}} {{.State.Running}} {{.State.Pid}}' "$candidate") || identity=
            pid=${identity##* }
            case "$pid" in ''|0|*[!0-9]*) pid= ;; esac
            if [ -n "$pid" ] && [ "$identity" = "$1 true $pid" ] &&
                nsenter -t "$pid" -n curl --noproxy '*' -fsS --max-time 3 "$url/readyz" >/dev/null 2>&1 &&
                [ "$(compose ps -q "$SERVICE")" = "$candidate" ]; then
                if [ -z "${3:-}" ] || [ "$(nsenter -t "$pid" -n curl --noproxy '*' -fsS --max-time 3 "$url/client/update-fingerprint" | jq -r '.fingerprint.id // ""')" = "$3" ]; then
                    container=$candidate
                    return 0
                fi
            fi
        fi
        attempts=$((attempts + 1))
        sleep 3
    done
    return 1
}

# remember_previous records the image a switch replaces, for rollback.
remember_previous() {
    printf '%s\n' "$1" >"$PREVIOUS_FILE.tmp" && mv -f "$PREVIOUS_FILE.tmp" "$PREVIOUS_FILE"
}

previous_image() {
    old=$(current_container) || return 1
    image=$(docker inspect --format '{{.Image}}' "$old") || return 1
    case "$image" in sha256:*) printf '%s %s\n' "$old" "$image" ;; *) return 1 ;; esac
}

# --- commands -----------------------------------------------------------------

cmd_install() {
    require docker jq flock curl nsenter openssl install stat
    docker compose version >/dev/null 2>&1 || die 'the Docker Compose plugin is required'
    install -d -m 700 -o root -g root "$BD2_DIR"
    if [ ! -e "$COMPOSE_FILE" ]; then
        template=$(dirname "$(readlink -f "$0")")/compose.yaml
        if [ -n "${1:-}" ]; then
            template=$1/tools/server/compose.yaml
        elif [ ! -f "$template" ]; then
            if [ ! -d "$SOURCE_DIR" ]; then
                require git
                git clone --quiet "$SOURCE_URL" "$SOURCE_DIR"
            fi
            template=$SOURCE_DIR/tools/server/compose.yaml
        fi
        [ -f "$template" ] || die "no Compose template at $template"
    fi
    install -d -m 700 -o 10001 -g 10001 "$DATA_DIR" "$UPDATES"
    install -d -m 755 -o 10001 -g 10001 "$DATA_DIR/config"
    if [ -n "$SEED_CONFIG_DIR" ]; then
        # The server and its /admin/ console own these files once they exist.
        for name in authentication.json resources.json game.json; do
            if [ -f "$SEED_CONFIG_DIR/$name" ] && [ ! -e "$DATA_DIR/config/$name" ]; then
                install -m 600 -o 10001 -g 10001 "$SEED_CONFIG_DIR/$name" "$DATA_DIR/config/$name"
                log "seeded $DATA_DIR/config/$name"
            fi
        done
    fi
    if [ ! -e "$ENV_FILE" ]; then
        (umask 077; printf 'BD2_AUTH_MASTER_KEY=%s\n' "$(openssl rand -base64 32)" >"$ENV_FILE")
        chown root:root "$ENV_FILE"
        log "created $ENV_FILE with a new authentication master key"
    fi
    [ "$(grep -c '^BD2_AUTH_MASTER_KEY=' "$ENV_FILE")" -eq 1 ] || die "$ENV_FILE needs exactly one BD2_AUTH_MASTER_KEY"
    if [ ! -e "$COMPOSE_FILE" ]; then
        install -m 644 "$template" "$COMPOSE_FILE"
        log "created $COMPOSE_FILE from the template; review its ports before exposing the server"
    fi
    if [ "$(readlink -f "$0")" != "$(readlink -f "$SELF" 2>/dev/null || true)" ]; then
        install -m 700 -o root -g root "$0" "$SELF"
    fi
    cat >/etc/systemd/system/bd2-docker-update.service <<EOF
[Unit]
Description=Switch bd2server to the image it requested
After=network-online.target docker.service
Wants=network-online.target
Requires=docker.service
StartLimitIntervalSec=300
StartLimitBurst=3

[Service]
Type=oneshot
UMask=0077
ExecStart=$SELF handoff
# Builds can exceed the default oneshot timeout.
TimeoutStartSec=30min
TimeoutStopSec=30
EOF
    cat >/etc/systemd/system/bd2-docker-update.path <<EOF
[Unit]
Description=Watch for bd2server image update requests

[Path]
# PathExists also catches requests written while this host was down.
PathExists=$UPDATES/docker-request.json
PathExists=$UPDATES/docker-request.processing.json
Unit=bd2-docker-update.service

[Install]
WantedBy=multi-user.target
EOF
    chmod 644 /etc/systemd/system/bd2-docker-update.service /etc/systemd/system/bd2-docker-update.path
    systemctl daemon-reload
    systemctl enable --now bd2-docker-update.path >/dev/null
    log "installed in $BD2_DIR"
}

cmd_deploy() {
    require docker jq flock curl nsenter
    mode=source
    source_path=$SOURCE_DIR
    case "$#" in
        0) ;;
        1) [ "$1" != --image ] || die 'usage: deploy --image REF'; source_path=$1 ;;
        2) [ "$1" = --image ] || die 'usage: deploy [SOURCE] | deploy --image REF'; mode=image; image_ref=$2 ;;
        *) die 'usage: deploy [SOURCE] | deploy --image REF' ;;
    esac
    lock
    [ ! -f "$UPDATES/docker-request.json" ] && [ ! -f "$UPDATES/docker-request.processing.json" ] ||
        die 'a server-requested update is pending; let it finish (or inspect it) before deploying'
    if [ "$mode" = image ]; then
        log "resolving image $image_ref"
        target=$(resolve_image "$image_ref") || die 'cannot resolve the deployment image'
    else
        require realpath
        log "building local source $source_path"
        target=$(build_source_image "$source_path") || die 'building the local source failed'
    fi
    printf '%s\n' "$target" | grep -Eq '^sha256:[0-9a-f]{64}$' || die 'build did not produce an immutable image ID'
    if replaced=$(previous_image); then
        old_container=${replaced% *}
        remember_previous "${replaced#* }"
    else
        old_container=
    fi
    log "starting $target"
    use_image "$target" || die 'switching the container failed'
    if ready "$target" "$old_container"; then
        log "deployed $target; the server is ready"
        cmd_status
        return
    fi
    compose logs --since 10m --tail 200 "$SERVICE" >&2 || true
    die "the new container did not become ready; inspect the logs above, then fix forward or run: $SELF rollback"
}

cmd_setup() {
    case "$#" in
        0|1) cmd_install "$@" ;;
        2) [ "$1" = --image ] || die 'usage: setup [SOURCE] | setup --image REF'; cmd_install ;;
        *) die 'usage: setup [SOURCE] | setup --image REF' ;;
    esac
    cmd_deploy "$@"
}

cmd_rollback() {
    require docker flock curl nsenter jq
    lock
    [ -f "$PREVIOUS_FILE" ] || die 'no previous image is recorded'
    previous=$(cat "$PREVIOUS_FILE")
    case "$previous" in sha256:*) ;; *) die "$PREVIOUS_FILE is invalid" ;; esac
    replaced=$(current_container) || replaced=
    log "starting the previous image $previous (save data is not rolled back)"
    use_image "$previous" || die 'switching the container failed'
    ready "$previous" "$replaced" || die 'the previous image did not become ready either; operator recovery is required'
    log 'rolled back; the server is ready'
}

cmd_status() {
    if container=$(current_container); then
        image=$(docker inspect --format '{{.Image}}' "$container")
        revision=$(docker image inspect --format '{{index .Config.Labels "org.opencontainers.image.revision"}}' "$image" 2>/dev/null || true)
        state=$(docker inspect --format '{{.State.Status}}' "$container")
        log "container $container ($state)"
        log "image $image${revision:+ built from $revision}"
    else
        log 'no container exists yet'
    fi
    [ ! -f "$PREVIOUS_FILE" ] || log "rollback image $(cat "$PREVIOUS_FILE")"
    [ ! -f "$UPDATES/docker-result.json" ] || log "last handoff $(jq -c '{to, outcome, message, at}' "$UPDATES/docker-result.json")"
}

# cmd_handoff serves DATA_DIR/updates/docker-request.json, written by a
# server that has warned players, drained and saved. The request is claimed
# by renaming it, and a result is always published before it is consumed, so
# the waiting server learns the outcome even after a failure.
cmd_handoff() {
    [ -d "$UPDATES" ] || exit 0
    require flock
    lock try
    request=$UPDATES/docker-request.json
    processing=$UPDATES/docker-request.processing.json
    result=$UPDATES/docker-result.json
    [ -f "$request" ] || [ -f "$processing" ] || exit 0
    command -v jq >/dev/null || die 'jq is required; request left untouched'

    outcome=switch_failed
    message='host update failed before switching containers'
    target='' container='' rollback='' from='' to='' commit='' request_at=''
    claimed=false switched=false

    publish_result() {
        result_tmp=$(mktemp "$UPDATES/.docker-result.XXXXXX") || return 1
        if jq -n --arg from "$from" --arg to "$to" --arg commit "$commit" \
            --arg request_at "$request_at" --arg outcome "${1:-$outcome}" --arg message "$message" \
            --arg at "$(date -u +%Y-%m-%dT%H:%M:%SZ)" --arg target "$target" \
            --arg container "$container" --arg rollback "$rollback" \
            '{from:$from,to:$to,commit:$commit,request_at:$request_at,outcome:$outcome,message:$message,at:$at,target:$target,container:$container,rollback:$rollback}' \
            >"$result_tmp" && chmod 0644 "$result_tmp" && mv -f "$result_tmp" "$result"; then
            return 0
        fi
        rm -f "$result_tmp"
        return 1
    }
    # shellcheck disable=SC2329 # invoked by the EXIT trap
    finish() {
        code=$?
        trap - EXIT HUP INT TERM
        if [ "$claimed" = true ]; then
            # A crash after starting a container may already have migrated saves.
            if [ "$switched" = true ] && [ "$outcome" = switch_failed ]; then
                outcome=needs_operator
                message='host handoff interrupted after the container switch; inspect containers and saves'
            fi
            if publish_result; then
                rm -f "$processing"
            else
                log 'cannot persist the result; the claimed request is kept for operator recovery'
                code=1
            fi
        fi
        log "$message"
        exit "$code"
    }
    trap finish EXIT
    trap 'exit 1' HUP INT TERM
    fail() { message=$1; exit 1; }
    field() { jq -r --arg name "$1" 'if (.[$name] | type) == "string" then .[$name] else "" end' "$processing" 2>/dev/null || true; }

    if [ -f "$processing" ]; then
        claimed=true
        from=$(field from) to=$(field to) commit=$(field commit) request_at=$(field at)
        outcome=needs_operator
        fail 'an interrupted host handoff was found; inspect containers and saves before requesting another update'
    fi
    mv "$request" "$processing"
    claimed=true
    from=$(field from) to=$(field to) commit=$(field commit) request_at=$(field at)
    jq -e '(.from | type == "string" and length > 0) and
        (.to | type == "string" and test("^[0-9A-Za-z.+_-]+$")) and
        (.commit | type == "string" and test("^[0-9a-f]{40}$")) and
        (.at | type == "string" and length > 0) and
        ((.image // "") | type == "string") and
        (.migration.kind == "none" or .migration.kind == "reversible" or .migration.kind == "irreversible")' \
        "$processing" >/dev/null 2>&1 || fail 'invalid Docker update request (full commit and migration kind required)'
    fingerprint=$(jq -r '.fingerprint_id // ""' "$processing")
    [ -z "$fingerprint" ] || printf '%s\n' "$fingerprint" | grep -Eq '^[0-9a-f]{64}$' || fail 'invalid target fingerprint'
    image=$(jq -r '.image // ""' "$processing")
    migration=$(jq -r '.migration.kind' "$processing")
    for dependency in docker curl nsenter mktemp stat cp; do
        command -v "$dependency" >/dev/null || fail "$dependency is required"
    done
    replaced=$(previous_image) || fail 'expected exactly one existing Compose container with an immutable image'
    old_container=${replaced% *}
    previous=${replaced#* }

    if [ -n "$image" ]; then
        printf '%s\n' "$image" | grep -Eq '^[A-Za-z0-9][A-Za-z0-9._:/-]*@sha256:[0-9a-f]{64}$' || fail 'image must be pinned by a sha256 repository digest'
        docker pull "$image" >/dev/null || fail 'pulling the requested image failed'
        target=$(docker image inspect --format '{{.Id}}' "$image") || fail 'cannot resolve the pulled image ID'
    else
        (require git && verify_source && resolve_commit "$commit" >/dev/null) || fail 'cannot resolve the release source commit'
        target=$(build_image "$commit") || fail 'building the requested commit failed'
    fi

    remember_previous "$previous"
    switched=true
    if use_image "$target" && ready "$target" "$old_container" "$fingerprint"; then
        outcome=succeeded
        message='the requested image is ready in the new container'
        exit 0
    fi
    outcome=needs_operator
    message='the new container failed to become ready; inspect containers and saves'
    if [ "$migration" = irreversible ]; then
        compose stop "$SERVICE" || true
        fail 'the new container failed; an irreversible migration forbids starting the previous image automatically'
    fi
    failed_container=$(compose ps -a -q "$SERVICE") || failed_container=
    # Release the old version's startup gate only after the failed container
    # stopped; a failed rollback overwrites this provisional outcome below.
    compose stop "$SERVICE" || fail 'cannot stop the failed container before rollback'
    rollback=$previous
    message='starting the previous image; host readiness verification is still pending'
    publish_result rolled_back || fail 'cannot publish the rollback startup permission'
    if use_image "$previous" && ready "$previous" "$failed_container"; then
        outcome=rolled_back
        message='the new container failed; the previous image is ready (save data was not restored)'
        exit 1
    fi
    compose stop "$SERVICE" || true
    fail 'the new container and the previous image both failed readiness; operator recovery is required'
}

[ "$(id -u)" -eq 0 ] || die 'run as root'
load_config
command=${1:-}
[ "$#" -eq 0 ] || shift
case "$command" in
    setup) cmd_setup "$@" ;;
    install) [ "$#" -le 1 ] || die 'usage: install [SOURCE]'; cmd_install "$@" ;;
    deploy) cmd_deploy "$@" ;;
    rollback) cmd_rollback ;;
    status) cmd_status ;;
    handoff) cmd_handoff ;;
    *) sed -n '2,15s/^# \{0,1\}//p' "$0"; exit 2 ;;
esac
