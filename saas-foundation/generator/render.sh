#!/usr/bin/env bash
# project-archetype renderer entry point.
#
#   render.sh validate --input FILE
#   render.sh plan     --input FILE --dest DIR [--report FILE]
#   render.sh render   --input FILE --dest DIR [--report FILE]
#
# Runs the stdlib-only Go renderer in the official golang image. Host
# requirements: bash, coreutils and Docker. No host Go is used.
# Exit codes: 0 ok; 1 internal error; 2 invalid input or unsafe destination
# (nothing written); 3 rendered with conflicts to reconcile.
set -euo pipefail

GO_IMAGE="golang:1.27.1-alpine"
CACHE_VOLUME="project-archetype-render-cache"

script_dir="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd -P)"
skill_dir="$(cd "${script_dir}/.." && pwd -P)"

die() { printf 'error: %s\n' "$1" >&2; exit "${2:-2}"; }

[[ $# -ge 1 ]] || die "usage: render.sh <validate|plan|render> --input FILE [--dest DIR] [--report FILE]"
cmd="$1"; shift
case "${cmd}" in validate|plan|render) ;; *) die "unknown command '${cmd}'";; esac

input="" dest="" report=""
while [[ $# -gt 0 ]]; do
  case "$1" in
    --input) input="${2:-}"; shift 2 ;;
    --dest) dest="${2:-}"; shift 2 ;;
    --report) report="${2:-}"; shift 2 ;;
    *) die "unknown argument '$1'" ;;
  esac
done

[[ -n "${input}" ]] || die "--input is required"
[[ -f "${input}" ]] || die "input file ${input} does not exist"
input_abs="$(cd "$(dirname "${input}")" && pwd -P)/$(basename "${input}")"

within() { # within PATH BASE
  [[ "$1" == "$2" || "$1" == "$2"/* ]]
}

docker_args=(--rm -v "${skill_dir}:/skill:ro" -v "${input_abs}:/work/input.json:ro" -v "${CACHE_VOLUME}:/cache:ro")
render_args=("${cmd}" --skill /skill --input /work/input.json)

if [[ "${cmd}" != "validate" ]]; then
  [[ -n "${dest}" ]] || die "--dest is required"
  [[ -e "${dest}" ]] || die "destination ${dest} does not exist"
  [[ ! -L "${dest}" ]] || die "destination must not be a symlink"
  [[ -d "${dest}" ]] || die "destination is not a directory"
  dest_abs="$(cd "${dest}" && pwd -P)"
  [[ "${dest_abs}" != "/" ]] || die "destination must not be the filesystem root"
  if within "${dest_abs}" "${skill_dir}"; then
    die "destination must not be the Skill package or inside it"
  fi
  docker_args+=(-v "${dest_abs}:/dest")
  render_args+=(--dest /dest)
  if [[ -n "${report}" ]]; then
    report_dir="$(cd "$(dirname "${report}")" 2>/dev/null && pwd -P)" || die "report directory does not exist"
    report_abs="${report_dir}/$(basename "${report}")"
    if within "${report_abs}" "${dest_abs}"; then
      die "--report must be outside the destination"
    fi
    docker_args+=(-v "${report_dir}:/report")
    render_args+=(--report "/report/$(basename "${report}")")
  fi
fi

# Build (or reuse) the renderer binary in a cache volume. The binary is keyed
# by a hash of the generator sources, so concurrent invocations from
# different Skill versions never run each other's binary. This step runs as
# the image's default user and touches only the cache volume.
src_hash="$(cat "${script_dir}"/*.go "${script_dir}/go.mod" | sha256sum | cut -c1-16)"
bin="/cache/bin/render-${src_hash}"
docker run --rm -v "${skill_dir}:/skill:ro" -v "${CACHE_VOLUME}:/cache" \
  -e GOCACHE=/cache/build -e GOFLAGS=-buildvcs=false -e GOTOOLCHAIN=local -e CGO_ENABLED=0 \
  -e BIN="${bin}" -w /skill/generator "${GO_IMAGE}" \
  sh -c 'mkdir -p /cache/bin && if [ ! -x "$BIN" ]; then tmp="$BIN.$$" && go build -o "$tmp" . && chmod 0755 "$tmp" && mv "$tmp" "$BIN"; fi' >&2

# Render as the invoking user so created files are owned by them.
exec docker run "${docker_args[@]}" --user "$(id -u):$(id -g)" -e HOME=/tmp \
  "${GO_IMAGE}" "${bin}" "${render_args[@]}"
