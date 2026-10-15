#!/bin/sh
# Host-side handoff. Install with the adjacent systemd units and run as root.
# Results live in DATA_DIR/updates/docker-result.json, including failed requests.
set -eu
umask 077

DATA_DIR=${BD2_DATA_DIR:-/opt/bd2/data}
SOURCE_DIR=${BD2_SOURCE_DIR:-/opt/bd2/src}
COMPOSE_DIR=${BD2_COMPOSE_DIR:-/opt/server-infra/bd2}
SERVICE=${BD2_COMPOSE_SERVICE:-server}
REMOTE_URL=${BD2_GIT_REMOTE_URL:-https://git.sighs.cc/Flechazo/bd2}
READY_URL=${BD2_READY_URL:-http://127.0.0.1:8080/readyz}
ENV_FILE=${BD2_ENV_FILE:-$COMPOSE_DIR/.env}
UPDATES="$DATA_DIR/updates"
REQUEST="$UPDATES/docker-request.json"
PROCESSING="$UPDATES/docker-request.processing.json"
RESULT="$UPDATES/docker-result.json"

log() { printf 'bd2-docker-update: %s\n' "$*"; }
[ -d "$UPDATES" ] || exit 0
command -v flock >/dev/null || { log 'flock is required'; exit 1; }
# Keep the inode: deleting a held lock allows another invocation to bypass it.
exec 9>>"$UPDATES/.docker-update.lock"
flock -n 9 || exit 0
[ -f "$REQUEST" ] || [ -f "$PROCESSING" ] || exit 0
command -v jq >/dev/null || { log 'jq is required; request left untouched'; exit 1; }

outcome=switch_failed
message='host update failed before switching containers'
target= container= rollback= env_tmp= build_dir=
from= to= commit= request_at=
claimed=false
switched=false

publish_result() {
    result_tmp=$(mktemp "$UPDATES/.docker-result.XXXXXX") || return 1
    if jq -n --arg from "$from" --arg to "$to" --arg commit "$commit" \
        --arg request_at "$request_at" --arg outcome "${1:-$outcome}" --arg message "$message" \
        --arg at "$(date -u +%Y-%m-%dT%H:%M:%SZ)" --arg target "$target" \
        --arg container "$container" --arg rollback "$rollback" \
        '{from:$from,to:$to,commit:$commit,request_at:$request_at,outcome:$outcome,message:$message,at:$at,target:$target,container:$container,rollback:$rollback}' \
        >"$result_tmp" && chmod 0644 "$result_tmp" && mv -f "$result_tmp" "$RESULT"; then
        return 0
    fi
    rm -f "$result_tmp"
    return 1
}
finish() {
    code=$?
    trap - EXIT HUP INT TERM
    if [ "$claimed" = true ]; then
        # A signal/crash after starting a container may have run save migrations.
        if [ "$switched" = true ] && [ "$outcome" = switch_failed ]; then
            outcome=needs_operator
            message='host handoff interrupted after container switch; inspect containers and saves'
        fi
        if publish_result; then
            # Publish the result before consuming the claimed request.
            rm -f "$PROCESSING"
        else
            log 'cannot persist result; processing request retained for operator recovery'
            code=1
        fi
    fi
    [ -z "$env_tmp" ] || rm -f "$env_tmp"
    if [ -n "$build_dir" ]; then
        git -C "$SOURCE_DIR" worktree remove --force "$build_dir/source" >/dev/null 2>&1 || true
        rmdir "$build_dir" 2>/dev/null || true
    fi
    log "$message"
    exit "$code"
}
trap finish EXIT
trap 'exit 1' HUP INT TERM
fail() { message=$1; exit 1; }
read_identity() {
    from=$(jq -r 'if (.from | type) == "string" then .from else "" end' "$PROCESSING" 2>/dev/null) || from=
    to=$(jq -r 'if (.to | type) == "string" then .to else "" end' "$PROCESSING" 2>/dev/null) || to=
    commit=$(jq -r 'if (.commit | type) == "string" then .commit else "" end' "$PROCESSING" 2>/dev/null) || commit=
    request_at=$(jq -r 'if (.at | type) == "string" then .at else "" end' "$PROCESSING" 2>/dev/null) || request_at=
}
if [ -f "$PROCESSING" ]; then
    claimed=true
    read_identity
    outcome=needs_operator
    fail 'an interrupted host handoff was found; inspect containers and saves before requesting another update'
fi
mv "$REQUEST" "$PROCESSING"
claimed=true
read_identity
jq -e '(.from | type == "string" and length > 0) and
    (.to | type == "string" and test("^[0-9A-Za-z.+_-]+$")) and
    (.commit | type == "string" and test("^[0-9a-f]{40}$")) and
    (.at | type == "string" and length > 0) and
    ((.image // "") | type == "string") and
    (.migration.kind == "none" or .migration.kind == "reversible" or .migration.kind == "irreversible")' \
    "$PROCESSING" >/dev/null 2>&1 || fail 'invalid Docker update request (full commit and migration kind required)'
image=$(jq -r '.image // ""' "$PROCESSING")
migration=$(jq -r '.migration.kind' "$PROCESSING")
for dependency in docker curl nsenter git mktemp stat cp; do
    command -v "$dependency" >/dev/null || fail "$dependency is required"
done
# This URL is resolved inside the new container's network namespace.
case "$READY_URL" in http://127.0.0.1:*/readyz) ;; *) fail 'BD2_READY_URL must use http://127.0.0.1:PORT/readyz' ;; esac
ready_port=${READY_URL#http://127.0.0.1:}
ready_port=${ready_port%/readyz}
case "$ready_port" in ''|*[!0-9]*) fail 'invalid readiness port' ;; esac
[ -f "$ENV_FILE" ] && [ ! -L "$ENV_FILE" ] || fail '.env must be an existing regular file'
# Retain its owner/group/mode exactly, including when it holds credentials.
env_metadata=$(stat -c '%u:%g:%a' "$ENV_FILE") || fail 'cannot inspect .env permissions'
compose() { docker compose --project-directory "$COMPOSE_DIR" --env-file "$ENV_FILE" "$@"; }
old_container=$(compose ps -a -q "$SERVICE") || fail 'cannot identify the current Compose container'
case "$old_container" in ''|*[!0-9a-f]*) fail 'expected exactly one existing Compose container' ;; esac
previous=$(docker inspect --format '{{.Image}}' "$old_container") || fail 'cannot inspect previous image'
case "$previous" in sha256:*) ;; *) fail 'previous container has no immutable image ID' ;; esac

# Verify a clean, expected repository and that the commit belongs to a currently
# advertised remote branch, even if an immutable image was supplied.
actual_url=$(git -C "$SOURCE_DIR" remote get-url --all origin) || fail 'cannot read source origin'
actual_url=${actual_url%/}
expected_url=${REMOTE_URL%/}
[ "${actual_url%.git}" = "${expected_url%.git}" ] || fail 'source origin does not match BD2_GIT_REMOTE_URL'
source_status=$(git -C "$SOURCE_DIR" status --porcelain --untracked-files=all) || fail 'cannot inspect source checkout'
[ -z "$source_status" ] || fail 'source checkout has local changes'
git -C "$SOURCE_DIR" fetch --quiet --no-tags --prune origin '+refs/heads/*:refs/bd2-update/*' || fail 'cannot fetch source origin'
[ "$(git -C "$SOURCE_DIR" rev-parse --verify "$commit^{commit}" 2>/dev/null)" = "$commit" ] || fail 'requested commit is not a commit in the source repository'
reachable=false
for ref in $(git -C "$SOURCE_DIR" for-each-ref --format='%(refname)' refs/bd2-update/); do
    if git -C "$SOURCE_DIR" merge-base --is-ancestor "$commit" "$ref"; then reachable=true; break; fi
done
[ "$reachable" = true ] || fail 'requested commit is not reachable from a fetched origin branch'
if [ -n "$image" ]; then
    printf '%s\n' "$image" | jq -Re 'test("^[A-Za-z0-9][A-Za-z0-9._:/-]*@sha256:[0-9a-f]{64}$")' >/dev/null || fail 'image must be pinned by a sha256 repository digest'
    docker pull "$image" >/dev/null || fail 'pulling the requested immutable image failed'
    target=$(docker image inspect --format '{{.Id}}' "$image") || fail 'cannot resolve pulled image ID'
else
    # A fresh checkout excludes ignored local files from the Docker build context.
    build_dir=$(mktemp -d) || fail 'cannot create build checkout'
    git -C "$SOURCE_DIR" -c core.hooksPath=/dev/null worktree add --quiet --detach "$build_dir/source" "$commit" || fail 'cannot check out the requested commit'
    target=$(docker build --quiet -f "$build_dir/source/Dockerfile.server" "$build_dir/source") || fail 'building the requested commit failed'
fi
printf '%s\n' "$target" | jq -Re 'test("^sha256:[0-9a-f]{64}$")' >/dev/null || fail 'Docker did not return one immutable image ID'

use_image() {
    env_tmp=$(mktemp "$ENV_FILE.update.XXXXXX") || return 1
    # Never source .env or print it. Prepare content privately, then restore metadata.
    { grep -v '^BD2_SERVER_IMAGE=' "$ENV_FILE" || [ "$?" -eq 1 ]; } >"$env_tmp" || return 1
    printf 'BD2_SERVER_IMAGE=%s\n' "$1" >>"$env_tmp" || return 1
    # GNU cp preserves the access ACL along with mode and ownership.
    cp --attributes-only --preserve=mode,ownership -- "$ENV_FILE" "$env_tmp" || return 1
    [ "$(stat -c '%u:%g:%a' "$env_tmp")" = "$env_metadata" ] || return 1
    mv -f "$env_tmp" "$ENV_FILE" || return 1
    env_tmp=
    compose up -d --no-deps --force-recreate --pull never "$SERVICE"
}
ready() {
    expected_image=$1
    replaced_id=$2
    attempts=0
    while [ "$attempts" -lt 60 ]; do
        candidate=$(compose ps -q "$SERVICE") || candidate=
        case "$candidate" in ''|*[!0-9a-f]*) candidate= ;; esac
        if [ -n "$candidate" ] && [ "$candidate" != "$replaced_id" ]; then
            identity=$(docker inspect --format '{{.Image}} {{.State.Running}} {{.State.Pid}}' "$candidate") || identity=
            pid=${identity##* }
            case "$pid" in ''|0|*[!0-9]*) pid= ;; esac
            if [ -n "$pid" ] && [ "$identity" = "$expected_image true $pid" ] &&
                nsenter -t "$pid" -n curl --noproxy '*' -fsS --max-time 3 "$READY_URL" >/dev/null 2>&1 &&
                [ "$(compose ps -q "$SERVICE")" = "$candidate" ] &&
                [ "$(docker inspect --format '{{.Image}} {{.State.Running}} {{.State.Pid}}' "$candidate")" = "$identity" ]; then
                container=$candidate
                return 0
            fi
        fi
        attempts=$((attempts + 1))
        sleep 3
    done
    return 1
}
switched=true
if use_image "$target" && ready "$target" "$old_container"; then
    outcome=succeeded
    message='requested image is ready in the new container'
    exit 0
fi
outcome=needs_operator
message='new container failed to become ready; inspect containers and saves'
if [ "$migration" = irreversible ]; then
    compose stop "$SERVICE" || true
    fail 'new container failed; irreversible migration forbids automatically starting the previous image'
fi
failed_container=$(compose ps -a -q "$SERVICE") || failed_container=
# Release the old version's startup gate only after the failed container has
# stopped; a failed rollback overwrites this provisional outcome below.
compose stop "$SERVICE" || fail 'cannot stop the failed container before rollback'
rollback=$previous
message='starting previous image; host readiness verification is still pending'
publish_result rolled_back || fail 'cannot publish rollback startup permission'
if use_image "$previous" && ready "$previous" "$failed_container"; then
    outcome=rolled_back
    rollback=$previous
    message='new container failed; previous image is ready (save data was not restored)'
    exit 1
fi
compose stop "$SERVICE" || true
fail 'new container and previous image failed readiness; operator recovery is required'
