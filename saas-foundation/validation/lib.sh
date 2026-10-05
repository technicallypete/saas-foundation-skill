#!/usr/bin/env bash
# Shared helpers for validation/run.sh. Host requirements: bash, coreutils,
# git and Docker (Compose >= 2.24). No host Go, Python, Node, curl or jq is used.
# shellcheck disable=SC2034  # variables are consumed by run.sh

YQ_IMAGE="mikefarah/yq:4.48.1"
BUSYBOX_IMAGE="busybox:1.37.0"
SHELLCHECK_IMAGE="koalaman/shellcheck:v0.11.0"
HADOLINT_IMAGE="hadolint/hadolint:v2.15.1"
GITLEAKS_IMAGE="zricethezav/gitleaks:v8.30.1"
GO_IMAGE="golang:1.27.1-alpine"

# Runtimes the project must never use from the host.
SHADOWED_RUNTIMES=(go gofmt python python3 pip pip3 pytest uv uvicorn node npm npx corepack pnpm yarn bun deno psql postgres pg_ctl redis-cli redis-server mc aws)

log() { printf '[%s] %s\n' "$(date +%H:%M:%S)" "$*"; }

# --- results -----------------------------------------------------------------
# record ID STATUS MESSAGE   (STATUS: PASS | FAIL | UNMET | INFO)
record() {
  local id="$1" status="$2" msg="${3:-}"
  printf '%s\t%s\t%s\n' "$id" "$status" "$msg" >>"$RESULTS"
  log "$status $id ${msg:+- $msg}"
}

# check ID MESSAGE COMMAND...  runs COMMAND and records PASS/FAIL. It always
# returns 0 so one failing check never aborts the remaining suites; the
# overall exit status is derived from the recorded results.
check() {
  local id="$1" msg="$2"; shift 2
  if "$@"; then record "$id" PASS "$msg"; else record "$id" FAIL "$msg"; fi
  return 0
}

# --- PATH hygiene and runtime shadows (T-DOCKER-ONLY) ---------------------------
setup_path_hygiene() {
  local cleaned="" entry
  IFS=':' read -r -a _parts <<<"$PATH"
  for entry in "${_parts[@]}"; do
    case "$entry" in /mnt/*|"") continue ;; esac
    cleaned="${cleaned:+$cleaned:}$entry"
  done
  SHADOW_DIR="$WORK/shadow-bin"
  SHADOW_LOG="$WORK/shadow.log"
  mkdir -p "$SHADOW_DIR"; : >"$SHADOW_LOG"
  local r
  for r in "${SHADOWED_RUNTIMES[@]}"; do
    for name in "$r" "$r.exe"; do
      cat >"$SHADOW_DIR/$name" <<EOF
#!/usr/bin/env bash
printf '%s %s\n' "$name" "\$*" >>"$SHADOW_LOG"
echo "forbidden host runtime invoked: $name" >&2
exit 127
EOF
      chmod +x "$SHADOW_DIR/$name"
    done
  done
  export PATH="$SHADOW_DIR:$cleaned"
}

# --- rendering -----------------------------------------------------------------
render() { # render CMD INPUT DEST [extra...]
  local cmd="$1" input="$2" dest="$3"; shift 3
  "$SKILL/generator/render.sh" "$cmd" --input "$input" --dest "$dest" "$@"
}

new_dir() { mktemp -d "$WORK/$1-XXXX"; }

output_id_of() { sed -n 's/^output_id\t//p' "$1" | tail -1; }

# tree_state DIR: content hash and mtime of every file (zero-write evidence).
tree_state() {
  (cd "$1" && find . -type f -printf '%p %T@ ' -exec sha256sum {} \; | LC_ALL=C sort)
}

# --- environment files -----------------------------------------------------------
random_hex() { head -c "${1:-20}" /dev/urandom | od -An -tx1 | tr -d ' \n'; }

# make_env DEST PROJECT: .env from .env.example with generated secrets and a
# unique Compose project name. Secrets are recorded for the canary scan only.
make_env() {
  local dest="$1" project="$2" line key value
  : >"$dest/.env"
  while IFS= read -r line || [[ -n "$line" ]]; do
    if [[ "$line" =~ ^([A-Z0-9_]+)=$ ]]; then
      key="${BASH_REMATCH[1]}"
      case "$key" in
        OPENROUTER_API_KEY) value="" ;;
        *) value="$(random_hex 20)"; printf '%s\n' "$value" >>"$SECRETS_FILE" ;;
      esac
      printf '%s=%s\n' "$key" "$value" >>"$dest/.env"
    elif [[ "$line" =~ ^COMPOSE_PROJECT_NAME= ]]; then
      printf 'COMPOSE_PROJECT_NAME=%s\n' "$project" >>"$dest/.env"
    else
      printf '%s\n' "$line" >>"$dest/.env"
    fi
  done <"$dest/.env.example"
}

env_get() { sed -n "s/^$2=//p" "$1/.env" | tail -1 | sed 's/^"\(.*\)"$/\1/'; }

env_set() { # env_set DEST KEY VALUE
  local tmp; tmp="$(mktemp)"
  awk -v k="$2" -v v="$3" 'BEGIN{FS=OFS="="} $1==k{print k "=" v; next} {print}' "$1/.env" >"$tmp" && mv "$tmp" "$1/.env"
}

compose() { # compose DEST args...  (the documented invocation)
  local dest="$1"; shift
  (cd "$dest" && docker compose --env-file .env -f infra/compose/local.yml "$@")
}

register_project() { printf '%s\n' "$1" >>"$WORK/projects"; }

# --- host probes (T-PORTS) ---------------------------------------------------------
PROBE_MODE=""
detect_probe() {
  if (exec 3<>/dev/tcp/127.0.0.1/1) 2>&1 | grep -q 'No such file or directory\|not supported'; then
    PROBE_MODE="curl"
  else
    PROBE_MODE="devtcp"
  fi
  if [[ "$PROBE_MODE" == curl ]] && ! command -v curl >/dev/null 2>&1; then PROBE_MODE="none"; fi
}

# http_get HOST PORT PATH -> prints "STATUS<TAB>BODY" (body on one line).
http_get() {
  local host="$1" port="$2" path="$3" out status
  if [[ "$PROBE_MODE" == devtcp ]]; then
    out="$(timeout 10 bash -c 'exec 3<>/dev/tcp/'"$host/$port"' || exit 9; printf "GET %s HTTP/1.0\r\nHost: %s\r\nConnection: close\r\n\r\n" "'"$path"'" "'"$host"'" >&3; cat <&3' 2>/dev/null)" || { printf '000\t\n'; return 0; }
    status="$(printf '%s' "$out" | head -1 | awk '{print $2}')"
    printf '%s\t%s\n' "${status:-000}" "$(printf '%s' "$out" | awk 'BEGIN{b=0} b{print} /^\r?$/{b=1}' | tr -d '\r\n')"
  elif [[ "$PROBE_MODE" == curl ]]; then
    out="$(curl -s -m 10 -w '\n%{http_code}' "http://$host:$port$path" 2>/dev/null)" || { printf '000\t\n'; return 0; }
    printf '%s\t%s\n' "$(printf '%s' "$out" | tail -1)" "$(printf '%s' "$out" | sed '$d' | tr -d '\r\n')"
  else
    printf 'NOPROBE\t\n'
  fi
}

# tcp_exchange HOST PORT HEXBYTES|TEXT MODE -> prints the first reply bytes.
tcp_redis_ping() {
  local host="$1" port="$2"
  if [[ "$PROBE_MODE" == devtcp ]]; then
    timeout 5 bash -c 'exec 3<>/dev/tcp/'"$host/$port"' || exit 9; printf "PING\r\n" >&3; head -c 7 <&3' 2>/dev/null
  elif [[ "$PROBE_MODE" == curl ]]; then
    printf 'PING\r\n' | timeout 5 curl -s -m 3 "telnet://$host:$port" 2>/dev/null | head -c 7
  fi
}
tcp_pg_sslrequest() {
  local host="$1" port="$2"
  if [[ "$PROBE_MODE" == devtcp ]]; then
    timeout 5 bash -c 'exec 3<>/dev/tcp/'"$host/$port"' || exit 9; printf "\x00\x00\x00\x08\x04\xd2\x16\x2f" >&3; head -c 1 <&3' 2>/dev/null
  elif [[ "$PROBE_MODE" == curl ]]; then
    printf '\x00\x00\x00\x08\x04\xd2\x16\x2f' | timeout 5 curl -s -m 3 "telnet://$host:$port" 2>/dev/null | head -c 1
  fi
}
tcp_open() { # tcp_open HOST PORT -> success if a TCP connection opens
  if [[ "$PROBE_MODE" == devtcp ]]; then
    timeout 5 bash -c 'exec 3<>/dev/tcp/'"$1/$2" 2>/dev/null
  else
    timeout 5 curl -s -m 3 "telnet://$1:$2" </dev/null >/dev/null 2>&1; [[ $? -ne 7 && $? -ne 28 ]]
  fi
}

host_nonloopback_ip() {
  local ip=""
  if command -v hostname >/dev/null 2>&1; then ip="$(hostname -I 2>/dev/null | awk '{print $1}')"; fi
  if [[ -z "$ip" ]] && command -v ip >/dev/null 2>&1; then ip="$(ip -4 -o addr show scope global 2>/dev/null | awk '{print $4}' | cut -d/ -f1 | head -1)"; fi
  printf '%s' "$ip"
}

# wait_http HOST PORT PATH STATUS SECONDS
wait_http() {
  local i
  for ((i = 0; i < $5; i++)); do
    [[ "$(http_get "$1" "$2" "$3" | cut -f1)" == "$4" ]] && return 0
    sleep 1
  done
  return 1
}

# --- in-network probes (busybox sharing a container's network namespace) ----------
cid_of() { compose "$1" ps -q "$2" | head -1; }
ns_tcp() { # ns_tcp CONTAINER_ID HOST PORT -> success if reachable
  docker run --rm --network "container:$1" "$BUSYBOX_IMAGE" nc -z -w 3 "$2" "$3" >/dev/null 2>&1
}
ns_resolves() { # ns_resolves CONTAINER_ID NAME
  docker run --rm --network "container:$1" "$BUSYBOX_IMAGE" nslookup "$2" >/dev/null 2>&1
}
container_ips() { # container_ips CONTAINER_ID -> all IPv4 addresses
  docker inspect -f '{{range .NetworkSettings.Networks}}{{.IPAddress}} {{end}}' "$1"
}

yq_stdin() { docker run --rm -i "$YQ_IMAGE" "$@"; }

# --- cleanup -----------------------------------------------------------------------
cleanup() {
  local p
  if [[ -f "$WORK/projects" ]]; then
    while IFS= read -r p; do
      [[ -n "$p" ]] || continue
      docker compose -p "$p" down -v --remove-orphans >/dev/null 2>&1 || true
    done <"$WORK/projects"
  fi
  if [[ "${KEEP:-0}" == 1 ]]; then log "keeping $WORK"; return; fi
  rm -rf "$WORK" 2>/dev/null || docker run --rm -v "$(dirname "$WORK"):/w" "$BUSYBOX_IMAGE" rm -rf "/w/$(basename "$WORK")" || true
}
