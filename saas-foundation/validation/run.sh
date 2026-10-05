#!/usr/bin/env bash
# saas-foundation validation harness.
#
#   validation/run.sh [--denylist FILE] [--repo-allow REGEX] [--keep] [--report-dir DIR] SUITE...
#   --repo-allow: extended regex of repository paths this change may touch
#                 (T-REPO-CLEAN); default: the Skill package only.
#   SUITE: static | generator | intelligence | isolation | trials | all
#
# Every suite renders its own disposable destinations outside the repository,
# so suites are independent. Host requirements: bash, coreutils, git, Docker
# with Compose >= 2.24. Host Go/Python/Node are shadowed and must never run.
# Exit status: 0 when every recorded check is PASS (or INFO), 1 otherwise.
set -euo pipefail

SKILL="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd -P)"
VALIDATION="$SKILL/validation"
# shellcheck source=lib.sh
source "$VALIDATION/lib.sh"

DENYLIST="" KEEP=0 REPORT_DIR="" REPO_ALLOW="^saas-foundation/" SUITES=()
while [[ $# -gt 0 ]]; do
  case "$1" in
    --denylist) DENYLIST="$(cd "$(dirname "$2")" && pwd -P)/$(basename "$2")"; shift 2 ;;
    --keep) KEEP=1; shift ;;
    --repo-allow) REPO_ALLOW="$2"; shift 2 ;;
    --report-dir) REPORT_DIR="$2"; shift 2 ;;
    static|generator|intelligence|isolation|trials) SUITES+=("$1"); shift ;;
    all) SUITES+=(static generator intelligence isolation trials); shift ;;
    *) echo "unknown argument: $1" >&2; exit 2 ;;
  esac
done
[[ ${#SUITES[@]} -gt 0 ]] || { echo "usage: run.sh [--denylist FILE] [--keep] [--report-dir DIR] static|generator|intelligence|isolation|trials|all" >&2; exit 2; }

REPO_ROOT="$(git -C "$SKILL" rev-parse --show-toplevel 2>/dev/null || true)"
RUN_ID="$(random_hex 3)"
WORK="$(mktemp -d "${TMPDIR:-/tmp}/archetype-validation-XXXXXX")"
if [[ -n "$REPO_ROOT" && "$WORK" == "$REPO_ROOT"/* ]]; then echo "work directory must be outside the repository" >&2; exit 2; fi
RESULTS="$WORK/results.tsv"; : >"$RESULTS"
SECRETS_FILE="$WORK/secrets"; : >"$SECRETS_FILE"
HARNESS_LOG="$WORK/harness.log"
trap cleanup EXIT
setup_path_hygiene
exec > >(tee -a "$HARNESS_LOG") 2>&1

log "run $RUN_ID; work $WORK; suites: ${SUITES[*]}"

# --- preflight ---------------------------------------------------------------------
compose_version="$(docker compose version --short 2>/dev/null || true)"
major="${compose_version%%.*}"; rest="${compose_version#*.}"; minor="${rest%%.*}"
if [[ -z "$compose_version" || "$major" -lt 2 || ( "$major" -eq 2 && "$minor" -lt 24 ) ]]; then
  record PREFLIGHT FAIL "Docker Compose >= 2.24 required (found '${compose_version:-none}')"; exit 1
fi
record PREFLIGHT INFO "docker $(docker version --format '{{.Server.Version}}'), compose $compose_version"
detect_probe
record PROBE INFO "host probe mode: $PROBE_MODE"

# Render the four variants used by several suites (render-only).
ALPHA_IN="$VALIDATION/inputs/alpha.json"; BRAVO_IN="$VALIDATION/inputs/bravo.json"
mk_variant_input() { # mk_variant_input SRC true|false OUT
  sed -E "s/\"intelligence_enabled\": *(true|false)/\"intelligence_enabled\": $2/" "$1" >"$3"
}
render_variants() {
  [[ -n "${V_ALPHA:-}" ]] && return 0
  mk_variant_input "$ALPHA_IN" false "$WORK/alpha-off.json"
  mk_variant_input "$BRAVO_IN" true "$WORK/bravo-on.json"
  V_ALPHA="$(new_dir v-alpha)"; V_BRAVO="$(new_dir v-bravo)"; V_ALPHA_OFF="$(new_dir v-alpha-off)"; V_BRAVO_ON="$(new_dir v-bravo-on)"
  render render "$ALPHA_IN" "$V_ALPHA" >/dev/null
  render render "$BRAVO_IN" "$V_BRAVO" >/dev/null
  render render "$WORK/alpha-off.json" "$V_ALPHA_OFF" >/dev/null
  render render "$WORK/bravo-on.json" "$V_BRAVO_ON" >/dev/null
  local d
  for d in "$V_ALPHA" "$V_BRAVO" "$V_ALPHA_OFF" "$V_BRAVO_ON"; do make_env "$d" "variant-$RUN_ID"; done
}

# ===================================================================================
suite_static() {
  log "== static"
  render_variants
  # The Skill is mounted under its own directory name (frontmatter name check).
  local go=(docker run --rm --user "$(id -u):$(id -g)" -e HOME=/tmp -e GOCACHE=/tmp/gocache -e GOTOOLCHAIN=local -e GOFLAGS=-buildvcs=false -v "$SKILL:/work/saas-foundation:ro" -w /work/saas-foundation/generator "$GO_IMAGE")

  check S-GO "generator gofmt, vet and unit tests" "${go[@]}" sh -c 'test -z "$(gofmt -l .)" && go vet ./... && go test -count=1 ./...'

  # Frontmatter parsed by a real YAML parser, then checked by the Go test.
  awk 'NR==1 && $0!="---"{exit 1} NR>1 && $0=="---"{exit} NR>1{print}' "$SKILL/SKILL.md" >"$WORK/frontmatter.yaml" || true
  if yq_stdin -o json <"$WORK/frontmatter.yaml" >"$WORK/frontmatter.json" 2>/dev/null; then
    check S-FRONTMATTER "SKILL.md frontmatter (yq-parsed) meets Agent Skills rules and section headings exist" \
      docker run --rm --user "$(id -u):$(id -g)" -e HOME=/tmp -e GOCACHE=/tmp/gocache -e GOTOOLCHAIN=local -e GOFLAGS=-buildvcs=false \
      -e FRONTMATTER_JSON=/fm/frontmatter.json -v "$WORK:/fm:ro" -v "$SKILL:/work/$(basename "$SKILL"):ro" -w "/work/$(basename "$SKILL")/generator" "$GO_IMAGE" \
      go test -count=1 -run 'TestSkillFrontmatter' ./...
  else
    record S-FRONTMATTER FAIL "SKILL.md frontmatter is not valid YAML"
  fi
  check S-LINKS "relative links resolve inside the Skill package" "${go[@]}" go test -count=1 -run 'TestSkillLinks' ./...
  check S-PATHS "no machine-specific paths or host-repository citations" "${go[@]}" go test -count=1 -run 'TestSkillPaths' ./...
  check S-TRACE "requirements catalog integrity" "${go[@]}" go test -count=1 -run 'TestSkillTrace' ./...

  # Shell scripts: harness, renderer and rendered scripts.
  local va="/work/${V_ALPHA#"$WORK"/}"
  local shells=(/skill/generator/render.sh /skill/validation/run.sh /skill/validation/lib.sh "$va/infra/postgres/init/10-roles.sh" "$va/infra/minio/init.sh")
  check S-SHELL "shellcheck on renderer, harness and rendered scripts" \
    docker run --rm -v "$SKILL:/skill:ro" -v "$WORK:/work:ro" "$SHELLCHECK_IMAGE" --severity=warning -x -P /skill/validation "${shells[@]}"

  local df ok=1
  for df in "$V_ALPHA"/infra/docker/*.Dockerfile; do
    docker run --rm -i "$HADOLINT_IMAGE" hadolint --failure-threshold warning - <"$df" >"$WORK/hadolint-$(basename "$df").txt" 2>&1 || { ok=0; cat "$WORK/hadolint-$(basename "$df").txt"; }
  done
  if [[ $ok == 1 ]]; then record S-DOCKERFILE PASS "hadolint clean for rendered Dockerfiles"; else record S-DOCKERFILE FAIL "hadolint findings"; fi

  ok=1
  for d in "$V_ALPHA" "$V_BRAVO" "$V_ALPHA_OFF" "$V_BRAVO_ON"; do compose "$d" config -q || ok=0; done
  [[ $ok == 1 ]] && record S-COMPOSE PASS "docker compose config -q for all four variants" || record S-COMPOSE FAIL "compose config failed"

  static_failclosed_env
  static_env_scope
  static_naming
  static_deps
  static_secrets
  static_leak
}

static_failclosed_env() {
  local d="$V_ALPHA" vars var ok=1 out
  mapfile -t vars < <(grep -o '\${[A-Z0-9_]*:?' "$d/infra/compose/local.yml" | sed 's/\${//; s/:?//' | sort -u)
  cp "$d/.env" "$WORK/env.backup"
  for var in "${vars[@]}"; do
    grep -v "^$var=" "$WORK/env.backup" >"$d/.env"
    if out="$(compose "$d" config -q 2>&1)"; then ok=0; log "removing $var did not fail";
    elif ! grep -q "$var" <<<"$out"; then ok=0; log "removing $var failed without naming it"; fi
  done
  cp "$WORK/env.backup" "$d/.env"
  [[ $ok == 1 ]] && record S-FAILCLOSED-ENV PASS "${#vars[@]} required variables each fail closed when missing" || record S-FAILCLOSED-ENV FAIL "a required variable did not fail closed"
}

static_env_scope() {
  local d ok=1 keys nets
  for d in "$V_ALPHA" "$V_BRAVO"; do
    compose "$d" config >"$WORK/config.yaml"
    keys="$(yq_stdin '.services.core.environment | keys | .[]' <"$WORK/config.yaml")"
    grep -qE '^(POSTGRES_SUPERUSER_PASSWORD|POSTGRES_PASSWORD|DB_MIGRATOR_PASSWORD|DB_RETENTION_PASSWORD|MINIO_ROOT_USER|MINIO_ROOT_PASSWORD)$' <<<"$keys" && { ok=0; log "core holds a privileged secret"; }
    keys="$(yq_stdin '.services.web.environment | keys | .[]' <"$WORK/config.yaml")"
    grep -vqE '^(PRODUCT_NAME|PLATFORM_BASE_DOMAIN|CUSTOM_DOMAIN_CNAME_TARGET|APP_ENV|CORE_INTERNAL_URL)$' <<<"$keys" && { ok=0; log "web holds an unexpected variable: $keys"; }
    nets="$(yq_stdin '.services.web.networks | keys | .[]' <"$WORK/config.yaml" | tr '\n' ' ')"
    [[ "$nets" == "edge " ]] || { ok=0; log "web networks: $nets"; }
    for svc in postgres redis minio; do
      nets="$(yq_stdin ".services.$svc.networks | keys | .[]" <"$WORK/config.yaml" | tr '\n' ' ')"
      [[ "$nets" == "data " ]] || { ok=0; log "$svc networks: $nets"; }
    done
    if yq_stdin -e '.services.intelligence' <"$WORK/config.yaml" >/dev/null 2>&1; then
      keys="$(yq_stdin '.services.intelligence.environment | keys | .[]' <"$WORK/config.yaml")"
      grep -qE '^(DB_|REDIS_|OBJECT_STORE_|MINIO_|POSTGRES_)' <<<"$keys" && { ok=0; log "intelligence holds data-store settings"; }
      nets="$(yq_stdin '.services.intelligence.networks | keys | .[]' <"$WORK/config.yaml" | tr '\n' ' ')"
      [[ "$nets" == "inference " ]] || { ok=0; log "intelligence networks: $nets"; }
    fi
  done
  rm -f "$WORK/config.yaml"
  [[ $ok == 1 ]] && record S-ENV-SCOPE PASS "per-service secrets and networks scoped (core, web, intelligence, data stores)" || record S-ENV-SCOPE FAIL "service scope violation"
}

static_naming() {
  local d ok=1 p
  for d in "$V_ALPHA" "$V_BRAVO" "$V_ALPHA_OFF" "$V_BRAVO_ON"; do
    for p in frontend backend api app services; do [[ -e "$d/$p" ]] && { ok=0; log "forbidden root $p"; }; done
    while IFS= read -r p; do
      p="${p#./}"
      IFS='/' read -r -a segs <<<"$p"
      for ((i = 1; i < ${#segs[@]}; i++)); do
        [[ "${segs[$i]}" == "${segs[$((i - 1))]}" ]] && { ok=0; log "redundant path $p"; }
      done
      case "$p" in core/internal/core*|core/cmd/core*|core/cmd/api*|intelligence/app*|intelligence/src/intelligence*) ok=0; log "redundant path $p" ;; esac
    done < <(cd "$d" && find . -mindepth 1 -not -path './.archetype*' -not -name '.env')
    if [[ -n "$(cd "$d" && find . -type d -empty -not -path './.archetype*')" ]]; then ok=0; log "empty directories in $d"; fi
  done
  [[ $ok == 1 ]] && record S-NAMING PASS "no forbidden roots, redundant segments or empty directories" || record S-NAMING FAIL "naming violation"
}

static_deps() {
  local ok=1 d="$V_ALPHA"
  [[ -z "$(find "$d/contracts" -type f ! -name '*.json' ! -name '*.md')" ]] || { ok=0; log "contracts holds non-contract files"; }
  grep -qE '"(pg|postgres|redis|ioredis|@aws-sdk/[a-z-]+|minio)"' "$d/web/package.json" && { ok=0; log "web depends on a data-store client"; }
  grep -qiE 'psycopg|asyncpg|sqlalchemy|redis|boto3|minio|aiobotocore' "$d/intelligence/pyproject.toml" && { ok=0; log "intelligence depends on a data-store client"; }
  grep -q '"core/internal/health"' "$d"/core/internal/config/*.go && { ok=0; log "config imports health"; }
  [[ $ok == 1 ]] && record S-DEPS PASS "service dependency direction (contracts, web, intelligence, core packages)" || record S-DEPS FAIL "dependency direction violation"
}

static_secrets() {
  local ok=1 d var
  for target in "$SKILL" "$V_ALPHA" "$V_BRAVO"; do
    docker run --rm -v "$target:/scan:ro" "$GITLEAKS_IMAGE" dir /scan --no-banner --redact --exit-code 1 \
      --config /scan/infra/security/gitleaks.toml >"$WORK/gitleaks.txt" 2>&1 \
      || docker run --rm -v "$target:/scan:ro" -v "$V_ALPHA/infra/security:/cfg:ro" "$GITLEAKS_IMAGE" dir /scan --no-banner --redact --exit-code 1 --config /cfg/gitleaks.toml >"$WORK/gitleaks.txt" 2>&1 \
      || { ok=0; cat "$WORK/gitleaks.txt"; }
  done
  # Build contexts never include local environment files (they may hold secrets).
  for di in "$V_ALPHA"/infra/docker/*.Dockerfile.dockerignore; do
    grep -qx '.env' "$di" && grep -qx '.env.\*' "$di" || { ok=0; log "$(basename "$di") does not exclude .env files"; }
  done
  for d in "$V_ALPHA" "$V_BRAVO"; do
    for var in POSTGRES_SUPERUSER_PASSWORD DB_MIGRATOR_PASSWORD DB_RUNTIME_PASSWORD DB_RETENTION_PASSWORD REDIS_PASSWORD MINIO_ROOT_PASSWORD OBJECT_STORE_SECRET_KEY INTELLIGENCE_SERVICE_TOKEN OPENROUTER_API_KEY; do
      if grep -qE "^$var=.+" "$d/.env.example"; then ok=0; log "$var not empty in .env.example"; fi
    done
  done
  [[ $ok == 1 ]] && record S-SECRETS PASS "gitleaks clean (Skill, alpha, bravo); every secret empty in .env.example" || record S-SECRETS FAIL "secret finding"
}

static_leak() {
  if [[ -z "$DENYLIST" ]]; then record S-LEAK UNMET "no --denylist given"; return; fi
  local terms ok=1 hits
  terms="$(grep -vE '^\s*(#|$)' "$DENYLIST" | paste -sd'|' -)"
  for target in "$SKILL" "$V_ALPHA" "$V_BRAVO" "$V_ALPHA_OFF" "$V_BRAVO_ON"; do
    hits="$(grep -rniwE --exclude-dir=node_modules -- "($terms)" "$target" || true)"
    if [[ -n "$hits" ]]; then ok=0; printf '%s\n' "$hits" | head -20; fi
  done
  [[ $ok == 1 ]] && record S-LEAK PASS "zero denylist hits in the Skill and all rendered variants" || record S-LEAK FAIL "denylisted term found"
}

# ===================================================================================
suite_generator() {
  log "== generator"
  local ok f dest before after out code

  # G-INVALID
  ok=1
  for f in "$VALIDATION"/inputs/invalid/*.json; do
    dest="$(new_dir inv)"; printf 'keep\n' >"$dest/sentinel"
    before="$(tree_state "$dest")"
    set +e; render render "$f" "$dest" >"$WORK/out.txt" 2>&1; code=$?; set -e
    after="$(tree_state "$dest")"
    if [[ $code -ne 2 || "$before" != "$after" ]]; then ok=0; log "$(basename "$f"): exit $code"; fi
  done
  # intelligence_enabled: null must be refused by every command, not only render.
  dest="$(new_dir inv-null)"; printf 'keep\n' >"$dest/sentinel"; before="$(tree_state "$dest")"
  set +e; "$SKILL/generator/render.sh" validate --input "$VALIDATION/inputs/invalid/null-intelligence-flag.json" >/dev/null 2>&1; code=$?; set -e
  [[ $code -eq 2 ]] || { ok=0; log "validate accepted intelligence_enabled: null ($code)"; }
  set +e; render plan "$VALIDATION/inputs/invalid/null-intelligence-flag.json" "$dest" >/dev/null 2>&1; code=$?; set -e
  [[ $code -eq 2 && "$before" == "$(tree_state "$dest")" ]] || { ok=0; log "plan accepted intelligence_enabled: null ($code)"; }
  set +e; render render "$ALPHA_IN" "$SKILL/references" >/dev/null 2>&1; code=$?; set -e
  [[ $code -eq 2 ]] || { ok=0; log "destination inside the Skill not refused ($code)"; }
  set +e; render render "$ALPHA_IN" "$WORK/does-not-exist" >/dev/null 2>&1; code=$?; set -e
  [[ $code -eq 2 && ! -e "$WORK/does-not-exist" ]] || { ok=0; log "missing destination not refused ($code)"; }
  [[ $ok == 1 ]] && record G-INVALID PASS "$(find "$VALIDATION/inputs/invalid" -name '*.json' | wc -l) invalid inputs (null intelligence_enabled also refused by validate and plan) + Skill/missing destinations: exit 2, zero writes" || record G-INVALID FAIL "invalid input not refused cleanly"

  # G-SENTINEL and G-REPEAT-CONFLICT (platform-shaped destination)
  dest="$(new_dir sentinel)"
  mkdir -p "$dest/docs" "$dest/management" "$dest/skills/other" "$dest/.git" "$dest/infra/compose"
  printf 'readme\n' >"$dest/README.md"; printf 'agents\n' >"$dest/AGENTS.md"; printf 'claude\n' >"$dest/CLAUDE.md"
  printf 'architecture\n' >"$dest/docs/ARCHITECTURE.md"; printf 'm\n' >"$dest/management/x.md"
  printf 'skill\n' >"$dest/skills/other/SKILL.md"; printf 'ref: refs/heads/main\n' >"$dest/.git/HEAD"
  printf 'SENTINEL_SECRET=keep\n' >"$dest/.env"; printf 'custom-ignore\n*.log' >"$dest/.gitignore"
  printf 'sentinel: true\n' >"$dest/infra/compose/local.yml"
  (cd "$dest" && find . -type f ! -name .gitignore -exec sha256sum {} \; | LC_ALL=C sort) >"$WORK/sentinel.sha"
  local gi_before; gi_before="$(cat "$dest/.gitignore")"
  set +e; render render "$ALPHA_IN" "$dest" >"$WORK/sentinel.out" 2>&1; code=$?; set -e
  local id; id="$(output_id_of "$WORK/sentinel.out")"
  ok=1
  [[ $code -eq 3 ]] || { ok=0; log "sentinel exit $code"; }
  (cd "$dest" && sha256sum -c --quiet "$WORK/sentinel.sha") || { ok=0; log "a sentinel changed"; }
  [[ "$(head -c ${#gi_before} "$dest/.gitignore")" == "$gi_before" ]] && grep -qx '.env' "$dest/.gitignore" || { ok=0; log ".gitignore not merged safely"; }
  grep -q $'^conflict\tinfra/compose/local.yml$' "$WORK/sentinel.out" || { ok=0; log "conflict not reported"; }
  [[ -f "$dest/.archetype/outputs/$id/proposed/infra/compose/local.yml" ]] || { ok=0; log "proposal missing"; }
  [[ $ok == 1 ]] && record G-SENTINEL PASS "platform-shaped destination: sentinels intact, .gitignore prefix preserved, conflict + proposal, exit 3" || record G-SENTINEL FAIL "sentinel check failed"
  before="$(tree_state "$dest")"; sleep 1
  set +e; render render "$ALPHA_IN" "$dest" >"$WORK/sentinel2.out" 2>&1; code=$?; set -e
  after="$(tree_state "$dest")"
  if [[ $code -eq 3 && "$(output_id_of "$WORK/sentinel2.out")" == "$id" && "$before" == "$after" ]] && grep -q $'^conflict\tinfra/compose/local.yml$' "$WORK/sentinel2.out"; then
    record G-REPEAT-CONFLICT PASS "repeat with unresolved conflicts: same output_id, conflicts re-reported, zero writes, exit 3"
  else record G-REPEAT-CONFLICT FAIL "exit $code"; fi
  SENTINEL_DEST="$dest"

  # G-REPEAT-CLEAN
  dest="$(new_dir clean)"
  render render "$BRAVO_IN" "$dest" >"$WORK/clean1.out" 2>&1 || true
  before="$(tree_state "$dest")"; sleep 1
  set +e; render render "$BRAVO_IN" "$dest" >"$WORK/clean2.out" 2>&1; code=$?; set -e
  after="$(tree_state "$dest")"
  if [[ $code -eq 0 && "$before" == "$after" && "$(output_id_of "$WORK/clean1.out")" == "$(output_id_of "$WORK/clean2.out")" ]] \
     && ! grep -vE $'^(unchanged\t|output_id\t|result\t)' "$WORK/clean2.out" | grep -q .; then
    record G-REPEAT-CLEAN PASS "repeat on clean destination: all unchanged (manifest included), hashes and mtimes identical, exit 0"
  else record G-REPEAT-CLEAN FAIL "exit $code"; fi


  # G-DETERMINISM
  local d1 d2; d1="$(new_dir det)"; d2="$(new_dir det)"
  render render "$ALPHA_IN" "$d1" >"$WORK/det1.out" 2>&1; render render "$ALPHA_IN" "$d2" >"$WORK/det2.out" 2>&1
  if diff -r "$d1" "$d2" >/dev/null && [[ "$(output_id_of "$WORK/det1.out")" == "$(output_id_of "$WORK/det2.out")" ]]; then
    record G-DETERMINISM PASS "identical input -> byte-identical trees and manifests, same output_id"
  else record G-DETERMINISM FAIL "renders differ"; fi

  # G-OUTPUT-ID: state independence + sensitivity
  ok=1
  local base_id ids=()
  for dest in "$(new_dir empty)" "$SENTINEL_DEST" "$d1"; do
    set +e; render plan "$ALPHA_IN" "$dest" >"$WORK/plan.out" 2>&1; set -e
    ids+=("$(output_id_of "$WORK/plan.out")")
  done
  base_id="${ids[0]}"
  for i in "${ids[@]}"; do [[ "$i" == "$base_id" && -n "$i" ]] || { ok=0; log "output_id differs by destination state"; }; done
  local mutations=(
    's/"Sample Atlas"/"Sample Atlas Two"/'
    's/"atlas.example.test"/"atlas2.example.test"/'
    's/"atlas_app"/"atlas_two"/'
    's/"intelligence_enabled": true/"intelligence_enabled": false/'
    's/^\{/{"project_slug": "other-slug",/'
    's/^\{/{"custom_domain_cname_target": "cname.example.net",/'
    's/^\{/{"identity_provider": "other-idp",/'
    's/^\{/{"production_object_store": "s3",/'
    's/^\{/{"ports": {"web": 3999},/'
  )
  for m in "${mutations[@]}"; do
    sed -E "$m" "$ALPHA_IN" >"$WORK/mut.json"
    "$SKILL/generator/render.sh" validate --input "$WORK/mut.json" >"$WORK/mut.out" 2>&1 || { ok=0; log "mutation invalid: $m"; continue; }
    [[ "$(output_id_of "$WORK/mut.out")" != "$base_id" ]] || { ok=0; log "mutation did not change output_id: $m"; }
  done
  [[ $ok == 1 ]] && record G-OUTPUT-ID PASS "output_id equal across empty/sentinel/rendered destinations; changes for each of ${#mutations[@]} input mutations" || record G-OUTPUT-ID FAIL "output identity check failed"

  # G-REPORT
  ok=1
  dest="$(new_dir report)"; mkdir -p "$WORK/reports"
  render render "$BRAVO_IN" "$dest" --report "$WORK/reports/r1.json" >"$WORK/r1.out" 2>&1 || ok=0
  local n_json n_out
  n_json="$(yq_stdin '.actions | length' <"$WORK/reports/r1.json")"
  n_out="$(grep -cvE $'^(output_id|result)\t' "$WORK/r1.out")"
  [[ "$n_json" == "$n_out" ]] || { ok=0; log "report actions $n_json != stdout $n_out"; }
  [[ "$(yq_stdin '.output_id' <"$WORK/reports/r1.json")" == "$(output_id_of "$WORK/r1.out")" ]] || ok=0
  render render "$BRAVO_IN" "$dest" --report "$WORK/reports/r2.json" >/dev/null 2>&1 || ok=0
  [[ "$(yq_stdin '[.actions[] | select(.action != "unchanged")] | length' <"$WORK/reports/r2.json")" == 0 ]] || ok=0
  set +e; render render "$BRAVO_IN" "$dest" --report "$dest/inside.json" >/dev/null 2>&1; code=$?; set -e
  [[ $code -eq 2 && ! -e "$dest/inside.json" ]] || { ok=0; log "report inside destination not refused"; }
  grep -rq '"action"' "$dest" && { ok=0; log "invocation actions found inside destination"; }
  [[ $ok == 1 ]] && record G-REPORT PASS "--report JSON matches stdout actions; never written inside the destination" || record G-REPORT FAIL "report check failed"

  # G-REFUSE
  ok=1
  local outside; outside="$(new_dir outside)"
  dest="$(new_dir refuse)"; ln -s "$outside" "$dest/infra"
  set +e; render render "$BRAVO_IN" "$dest" >/dev/null 2>&1; code=$?; set -e
  [[ $code -eq 2 && -z "$(ls -A "$outside")" ]] || { ok=0; log "symlinked parent not refused"; }
  dest="$(new_dir refuse)"; mkdir -p "$dest/docs/foundation"; ln -s "$outside/x" "$dest/docs/foundation/VENDORS.md"
  set +e; render render "$BRAVO_IN" "$dest" >/dev/null 2>&1; code=$?; set -e
  [[ $code -eq 2 && ! -e "$outside/x" ]] || { ok=0; log "symlinked target not refused"; }
  dest="$(new_dir refuse)"; ln -s "$outside" "$dest/link"
  set +e; render render "$BRAVO_IN" "$dest/link" >/dev/null 2>&1; code=$?; set -e
  [[ $code -eq 2 && -z "$(ls -A "$outside")" ]] || { ok=0; log "symlinked destination not refused"; }
  [[ $ok == 1 ]] && record G-REFUSE PASS "symlinked target, parent and destination refused with exit 2 and no writes" || record G-REFUSE FAIL "refusal failed"

  # G-TOGGLE-RERENDER
  ok=1
  dest="$(new_dir toggle)"
  mk_variant_input "$BRAVO_IN" true "$WORK/bravo-on-toggle.json"
  render render "$BRAVO_IN" "$dest" >"$WORK/t1.out" 2>&1 || ok=0
  local id1 id2; id1="$(output_id_of "$WORK/t1.out")"
  local m1; m1="$(sha256sum "$dest/.archetype/outputs/$id1/manifest.json")"
  set +e; render render "$WORK/bravo-on-toggle.json" "$dest" >"$WORK/t2.out" 2>&1; code=$?; set -e
  id2="$(output_id_of "$WORK/t2.out")"
  [[ $code -eq 3 && "$id1" != "$id2" ]] || { ok=0; log "toggle exit $code"; }
  grep -q $'^create\tintelligence/pyproject.toml$' "$WORK/t2.out" || ok=0
  grep -q $'^conflict\t.env.example$' "$WORK/t2.out" && grep -q $'^conflict\tinfra/compose/local.yml$' "$WORK/t2.out" || ok=0
  [[ -f "$dest/.archetype/outputs/$id2/proposed/.env.example" ]] || ok=0
  [[ "$(sha256sum "$dest/.archetype/outputs/$id1/manifest.json")" == "$m1" ]] || ok=0
  grep -q 'AI_MODEL_' "$dest/.env.example" && ok=0
  [[ $ok == 1 ]] && record G-TOGGLE-RERENDER PASS "enable intelligence later: new output_id, intelligence files created, .env.example/local.yml conflicts proposed, nothing overwritten" || record G-TOGGLE-RERENDER FAIL "toggle re-render failed"
}

# ===================================================================================
suite_intelligence() {
  log "== intelligence"
  render_variants
  local tag="archetype-intel-check-$RUN_ID" ok=1 id
  if ! docker build -q --target check --build-context "contracts=$V_ALPHA/contracts" \
      -f "$V_ALPHA/infra/docker/intelligence.Dockerfile" -t "$tag" "$V_ALPHA/intelligence" >"$WORK/intel-build.txt" 2>&1; then
    cat "$WORK/intel-build.txt"
    for id in I-CONFIG I-GATEWAY I-AUTH I-EXECUTE I-OBSERVABILITY I-SCHEMA I-LINT; do record "$id" FAIL "check image build failed"; done
    return
  fi
  local pairs=("I-CONFIG:i_config" "I-GATEWAY:i_gateway" "I-AUTH:i_auth" "I-EXECUTE:i_execute" "I-OBSERVABILITY:i_observability" "I-SCHEMA:i_schema")
  for p in "${pairs[@]}"; do
    id="${p%%:*}"
    if docker run --rm --network none "$tag" python -m pytest -q -k "${p#*:}" >"$WORK/intel-$id.txt" 2>&1 \
       && grep -qE '[0-9]+ passed' "$WORK/intel-$id.txt"; then
      record "$id" PASS "$(grep -oE '[0-9]+ passed' "$WORK/intel-$id.txt" | tail -1) (pytest -k ${p#*:}, --network none)"
    else cat "$WORK/intel-$id.txt"; record "$id" FAIL "pytest -k ${p#*:}"; fi
  done
  if docker run --rm --network none "$tag" sh -c 'ruff check --no-cache . && ruff format --check --no-cache .' >"$WORK/intel-lint.txt" 2>&1; then
    record I-LINT PASS "ruff check + format check"
  else cat "$WORK/intel-lint.txt"; record I-LINT FAIL "ruff"; fi
  docker image rm -f "$tag" >/dev/null 2>&1 || true
}

# ===================================================================================
suite_isolation() {
  log "== isolation"
  local rendered; rendered="$(new_dir iso-rendered)"
  render render "$ALPHA_IN" "$rendered" >/dev/null
  local out; out="$(new_dir iso-out)"; chmod 0777 "$out"
  export ISO_PROJECT="archetype-iso-$RUN_ID" ISO_DB="fixture_app" ISO_RENDERED="$rendered" ISO_SQL="$VALIDATION/isolation/sql" \
    ISO_MODULE="$VALIDATION/isolation" ISO_REFERENCES="$SKILL/references" ISO_OUT="$out" ISO_BUCKET="fixture-objects" ISO_S3_ACCESS_KEY="core-service"
  ISO_SUPERUSER_PASSWORD="$(random_hex)"; ISO_MIGRATOR_PASSWORD="$(random_hex)"; ISO_RUNTIME_PASSWORD="$(random_hex)"
  ISO_RETENTION_PASSWORD="$(random_hex)"; ISO_MINIO_ROOT_PASSWORD="$(random_hex)"; ISO_S3_SECRET_KEY="$(random_hex)"
  export ISO_SUPERUSER_PASSWORD ISO_MIGRATOR_PASSWORD ISO_RUNTIME_PASSWORD ISO_RETENTION_PASSWORD ISO_MINIO_ROOT_PASSWORD ISO_S3_SECRET_KEY
  printf '%s\n' "$ISO_SUPERUSER_PASSWORD" "$ISO_MIGRATOR_PASSWORD" "$ISO_RUNTIME_PASSWORD" "$ISO_RETENTION_PASSWORD" "$ISO_MINIO_ROOT_PASSWORD" "$ISO_S3_SECRET_KEY" >>"$SECRETS_FILE"
  register_project "$ISO_PROJECT"
  local ic=(docker compose -f "$VALIDATION/compose.yml")
  if ! "${ic[@]}" up -d --build --wait pg pg-restore minio >"$WORK/iso-up.txt" 2>&1 || ! "${ic[@]}" up --no-deps minio-init >>"$WORK/iso-up.txt" 2>&1; then
    tail -30 "$WORK/iso-up.txt"; record X-ENV FAIL "isolation environment did not start"; return
  fi
  record X-ENV INFO "PostgreSQL x2 initialized by the rendered role script; MinIO built from the rendered minio.Dockerfile"

  local db="$ISO_DB" vars f as
  apply_sql() { # apply_sql SERVICE
    local svc="$1"
    for f in "$ISO_SQL"/*.sql; do
      [[ "$(basename "$f")" == 40_seed.sql && "${2:-}" == noseed ]] && continue
      as="$(head -1 "$f" | sed 's/.*apply-as: //')"
      vars=(-v ON_ERROR_STOP=1 -v "owner_role=${db}_owner" -v "runtime_role=${db}_runtime" -v "retention_role=${db}_retention" -v "policy_reader_role=${db}_policy_reader")
      if [[ "$as" == superuser ]]; then
        "${ic[@]}" exec -T "$svc" psql -q -U postgres -d "$db" "${vars[@]}" -f "/sql/$(basename "$f")" >/dev/null || return 1
      else
        "${ic[@]}" exec -T -e PGPASSWORD="$ISO_MIGRATOR_PASSWORD" "$svc" psql -q -h 127.0.0.1 -U "${db}_migrator" -d "$db" "${vars[@]}" -f "/sql/$(basename "$f")" >/dev/null || return 1
      fi
    done
  }
  if ! apply_sql pg; then record X-SCHEMA FAIL "fixture SQL did not apply"; return; fi
  record X-SCHEMA PASS "fixture SQL applied (owner objects via migrator SET ROLE; superuser for roles, definer ownership and seed only)"

  # Full suite against the primary instance.
  set +e
  "${ic[@]}" run --rm -T runner sh -c 'go vet ./... && go test -count=1 -v ./... 2>&1' >"$WORK/iso-test.txt" 2>&1
  local rc=$?
  set -e
  cp "$WORK/iso-test.txt" "$out/iso-test.txt" 2>/dev/null || true
  # One check ID per fixture package (literal IDs; the requirements catalog references them).
  local pair pkg idp line
  for pair in "internal/fixture:X-FIXTURE" "rls:X-RLS" "policy:X-POLICY" "hostname:X-HOSTNAME" "mcp:X-MCP" "storage:X-STORAGE"; do
    pkg="${pair%%:*}"; idp="${pair##*:}"
    line="$(grep -E "^(ok|FAIL)\s+example.invalid/archetype/isolation/$pkg(\s|$)" "$WORK/iso-test.txt" | tail -1 || true)"
    if [[ "$line" == ok* ]]; then record "$idp" PASS "go test ./$pkg: $line"
    else record "$idp" FAIL "go test ./$pkg: ${line:-no result}"; fi
  done
  [[ $rc -eq 0 ]] || { grep -E -- '--- FAIL|FAIL\s' "$WORK/iso-test.txt" | head -40; }
  grep -E '^\s*--- (PASS|FAIL|SKIP): Test' "$WORK/iso-test.txt" | sed -E 's/^\s*--- (PASS|FAIL|SKIP): (Test[^ ]+).*/\2\t\1/' | LC_ALL=C sort -u >"$out/tests.tsv"
  if grep -q $'\tSKIP' "$out/tests.tsv"; then record X-SKIPS FAIL "isolation tests skipped while integration is required"; else record X-SKIPS PASS "no isolation test skipped"; fi


  # X-BKP: dump, restore into a second instance initialized from the same role script, re-run a subset.
  if "${ic[@]}" exec -T pg pg_dump -U postgres -d "$db" -Fc -n app >"$WORK/fixture.dump" \
     && "${ic[@]}" exec -T pg-restore psql -q -U postgres -d "$db" -v ON_ERROR_STOP=1 -v "policy_reader_role=${db}_policy_reader" -f /sql/00_roles.sql >/dev/null \
     && "${ic[@]}" exec -T pg-restore pg_restore -U postgres -d "$db" --clean --if-exists --exit-on-error <"$WORK/fixture.dump" \
     && ISO_PG_HOST=pg-restore "${ic[@]}" run --rm -T runner go test -count=1 -v ./rls/... -run '^(TestRuntime_Precondition|TestRuntime_RLSCoverage|TestOwner_ForcedRLS|TestRuntime_ReadIsolation|TestRuntime_NoContext|TestRuntime_AuditPrivileges|TestRuntime_DefinerAllowlist)$' >"$WORK/iso-bkp.txt" 2>&1 \
     && [[ "$(grep -c -- '--- PASS' "$WORK/iso-bkp.txt")" -ge 7 ]]; then
    record X-BKP PASS "pg_dump -> restore into second instance (same rendered role script); $(grep -c -- '--- PASS' "$WORK/iso-bkp.txt") restore-subset tests pass"
  else
    tail -30 "$WORK/iso-bkp.txt" 2>/dev/null || true; record X-BKP FAIL "backup/restore smoke"
  fi
  "${ic[@]}" logs --no-color >"$WORK/compose-logs-iso.txt" 2>&1 || true
  "${ic[@]}" down -v --remove-orphans >/dev/null 2>&1 || true
  docker run --rm -v "$out:/out" "$BUSYBOX_IMAGE" chown -R "$(id -u):$(id -g)" /out >/dev/null 2>&1 || true
  [[ -n "$REPORT_DIR" ]] && { mkdir -p "$REPORT_DIR"; cp "$out"/tests.tsv "$out"/labels.tsv "$WORK/iso-test.txt" "$WORK/iso-bkp.txt" "$REPORT_DIR/" 2>/dev/null || true; }
}

# ===================================================================================
# Executable documentation blocks.
docs_block() { # docs_block DEST NAME -> commands of the fenced block after <!-- archetype:NAME -->
  awk -v m="<!-- archetype:$2 -->" '$0==m{f=1; next} f && /^```sh/{c=1; next} c && /^```/{exit} c{print}' "$1/docs/foundation/GETTING-STARTED.md"
}
run_docs_block() { # run_docs_block DEST NAME LOG
  local cmds; cmds="$(docs_block "$1" "$2")"
  [[ -n "$cmds" ]] || { log "no $2 block"; return 1; }
  printf '%s\n' "$cmds" >"$3.cmds"
  (cd "$1" && bash -euo pipefail -c "$cmds") >"$3" 2>&1
}

trial_start() { # trial_start NAME DEST INPUT
  local name="$1" dest="$2" input="$3" project="$4"
  render render "$input" "$dest" >"$WORK/$name-render.out" 2>&1
  make_env "$dest" "$project"
  register_project "$project"
}

ports_busy() { local p; for p in "$@"; do tcp_open 127.0.0.1 "$p" && return 0; done; return 1; }

suite_trials() {
  log "== trials"
  render_variants
  trials_portability
  [[ "$PROBE_MODE" != none ]] || record PROBE UNMET "neither bash /dev/tcp nor curl available for host probes"

  local A; A="$(new_dir trial-alpha)"
  trial_start alpha "$A" "$ALPHA_IN" "atlas-$RUN_ID"
  local B; B="$(new_dir trial-bravo)"
  trial_start bravo "$B" "$BRAVO_IN" "harbor-$RUN_ID"

  # ---- alpha (intelligence enabled, default ports)
  local aports=(3100 8180 8100 5532 6479 9100 9101)
  if ports_busy "${aports[@]}"; then
    record T-DOCS-EXEC FAIL "alpha default host ports are busy; refusing to re-map silently"; return
  fi
  if run_docs_block "$A" start "$WORK/alpha-start.log"; then record T-DOCS-EXEC-START-ALPHA PASS "documented start block (alpha)"
  else tail -40 "$WORK/alpha-start.log"; record T-DOCS-EXEC FAIL "alpha start block failed"; return; fi

  trials_ports alpha "$A" 3100 8180 5532 6479 9100 9101 8100
  trials_boundaries "$A" yes
  trials_boundary_controls "$A"
  trials_outage "$A" 8180 3100 yes
  if run_docs_block "$A" check "$WORK/alpha-check.log"; then record T-CHECKS-ALPHA PASS "documented check block (core, web, intelligence, secret-scan)"
  else tail -40 "$WORK/alpha-check.log"; record T-CHECKS-ALPHA FAIL "alpha check block"; fi
  trials_persist "$A" 8180
  trials_rename "$A" 8180 3100
  compose "$A" logs --no-color >"$WORK/compose-logs-alpha.txt" 2>&1 || true
  if run_docs_block "$A" stop "$WORK/alpha-stop.log"; then record T-DOCS-EXEC-STOP-ALPHA PASS "documented stop block (alpha)"; else record T-DOCS-EXEC FAIL "alpha stop block"; fi
  compose "$A" down -v >/dev/null 2>&1 || true

  # ---- bravo (intelligence disabled, overridden ports)
  if run_docs_block "$B" start "$WORK/bravo-start.log"; then record T-DOCS-EXEC-START-BRAVO PASS "documented start block (bravo, overridden ports)"
  else tail -40 "$WORK/bravo-start.log"; record T-DOCS-EXEC FAIL "bravo start block failed"; return; fi
  trials_ports bravo "$B" 3200 8280 5632 6579 9200 9201
  trials_boundaries "$B" no
  trials_toggle "$B"
  if run_docs_block "$B" check "$WORK/bravo-check.log"; then record T-CHECKS-BRAVO PASS "documented check block (core, web, secret-scan)"
  else tail -40 "$WORK/bravo-check.log"; record T-CHECKS-BRAVO FAIL "bravo check block"; fi
  compose "$B" logs --no-color >"$WORK/compose-logs-bravo.txt" 2>&1 || true
  if run_docs_block "$B" stop "$WORK/bravo-stop.log"; then record T-DOCS-EXEC-STOP-BRAVO PASS "documented stop block (bravo)"; else record T-DOCS-EXEC FAIL "bravo stop block"; fi
  compose "$B" down -v >/dev/null 2>&1 || true
  if ! grep -q $'^T-DOCS-EXEC\tFAIL' "$RESULTS"; then record T-DOCS-EXEC PASS "start/check/stop blocks executed verbatim for alpha and bravo"; fi

}

trials_portability() {
  local ok=1 lit d
  local excl=(-x .env.example -x .env -x .archetype -x VENDORS.md)
  diff -r "${excl[@]}" "$V_ALPHA" "$V_BRAVO_ON" >"$WORK/diff-on.txt" || { ok=0; head -20 "$WORK/diff-on.txt"; }
  diff -r "${excl[@]}" "$V_ALPHA_OFF" "$V_BRAVO" >"$WORK/diff-off.txt" || { ok=0; head -20 "$WORK/diff-off.txt"; }
  # Literal confinement: identity values only in .env.example and .archetype.
  local alpha_lits=("Sample Atlas" "sample-atlas" "atlas.example.test" "atlas_app")
  local bravo_lits=("Harbor Desk" "harbor-desk" "harbor.example.org" "domains.harbor.example.org" "harbor_desk")
  for d in "$V_ALPHA" "$V_ALPHA_OFF"; do
    for lit in "${alpha_lits[@]}"; do
      if grep -rliF --exclude=.env.example --exclude=.env --exclude-dir=.archetype -- "$lit" "$d" | grep -q .; then ok=0; log "alpha literal '$lit' outside .env.example"; fi
    done
    for lit in "${bravo_lits[@]}"; do grep -rliF -- "$lit" "$d" | grep -v '/.env$' | grep -q . && { ok=0; log "bravo literal '$lit' in alpha output"; }; done
  done
  for d in "$V_BRAVO" "$V_BRAVO_ON"; do
    for lit in "${bravo_lits[@]}"; do
      if grep -rliF --exclude=.env.example --exclude=.env --exclude-dir=.archetype -- "$lit" "$d" | grep -q .; then ok=0; log "bravo literal '$lit' outside .env.example"; fi
    done
    for lit in "${alpha_lits[@]}"; do grep -rliF -- "$lit" "$d" | grep -v '/.env$' | grep -q . && { ok=0; log "alpha literal '$lit' in bravo output"; }; done
  done
  [[ $ok == 1 ]] && record T-PORTABILITY PASS "2x2 matrix: sources byte-identical except allowlist; identity literals confined to .env.example/.archetype" || record T-PORTABILITY FAIL "portability violation"
}

trials_ports() { # trials_ports NAME DEST WEB CORE PG REDIS MINIO_API MINIO_CONSOLE [INTELLIGENCE]
  local name="$1" dest="$2" web="$3" core="$4" pg="$5" redis="$6" mapi="$7" mcon="$8" intel="${9:-}"
  local ok=1 svc port pub
  local pairs=("web:3000:$web" "core:8180:$core" "postgres:5432:$pg" "redis:6379:$redis" "minio:9000:$mapi" "minio:9001:$mcon")
  [[ -n "$intel" ]] && pairs+=("intelligence:8000:$intel")
  for p in "${pairs[@]}"; do
    IFS=: read -r svc port pub <<<"$p"
    [[ "$(compose "$dest" port "$svc" "$port" 2>/dev/null)" == "127.0.0.1:$pub" ]] || { ok=0; log "$svc:$port published as '$(compose "$dest" port "$svc" "$port" 2>/dev/null)'"; }
  done
  [[ $ok == 1 ]] && record "T-PORTS-CONFIG-$name" PASS "docker compose port: every service on 127.0.0.1 at its configured port (configuration evidence only)" || record "T-PORTS-CONFIG-$name" FAIL "port mapping mismatch"

  if [[ "$PROBE_MODE" == none ]]; then record "T-PORTS-$name" UNMET "no host probe available; configuration evidence is not reachability"; return; fi
  ok=1
  [[ "$(http_get 127.0.0.1 "$web" /api/health | cut -f1)" == 200 ]] || { ok=0; log "web /api/health"; }
  [[ "$(http_get 127.0.0.1 "$core" /healthz | cut -f1)" == 200 ]] || { ok=0; log "core /healthz"; }
  [[ -z "$intel" || "$(http_get 127.0.0.1 "$intel" /healthz | cut -f1)" == 200 ]] || { ok=0; log "intelligence /healthz"; }
  [[ "$(http_get 127.0.0.1 "$mapi" /minio/health/live | cut -f1)" == 200 ]] || { ok=0; log "minio api"; }
  [[ "$(http_get 127.0.0.1 "$mcon" / | cut -f1)" =~ ^[1-5][0-9][0-9]$ ]] || { ok=0; log "minio console"; }
  [[ "$(tcp_redis_ping 127.0.0.1 "$redis")" == "-NOAUTH" ]] || { ok=0; log "redis did not answer -NOAUTH"; }
  [[ "$(tcp_pg_sslrequest 127.0.0.1 "$pg")" =~ ^[NS]$ ]] || { ok=0; log "postgres SSLRequest"; }
  local ip; ip="$(host_nonloopback_ip)"
  if [[ -n "$ip" ]]; then
    for port in "$web" "$core" "$pg" "$redis" "$mapi" "$mcon" $intel; do
      tcp_open "$ip" "$port" && { ok=0; log "port $port reachable on non-loopback $ip"; }
    done
  else ok=0; log "no non-loopback address to prove loopback-only binding"; fi
  local core_cid; core_cid="$(cid_of "$dest" core)"
  for p in postgres:5432 redis:6379 minio:9000; do ns_tcp "$core_cid" "${p%%:*}" "${p##*:}" || { ok=0; log "core cannot reach $p internally"; }; done
  [[ $ok == 1 ]] && record "T-PORTS-$name" PASS "host reachability ($PROBE_MODE): web, core$([[ -n $intel ]] && echo ', intelligence'), MinIO API/console HTTP; Redis -NOAUTH; PostgreSQL SSLRequest; refused on $ip; internal DNS/ports intact" || record "T-PORTS-$name" FAIL "reachability check failed"
}

# Each datastore is probed on its own service port.
datastore_port() {
  case "$1" in postgres) echo 5432 ;; redis) echo 6379 ;; minio) echo 9000 ;; *) return 1 ;; esac
}

# forbidden_datastores_reachable PROBE_CID DEST [SVC...]
# Probes every datastore independently, by service name and by each of its
# container IPs, on that datastore's own port. Prints one line per reachable
# endpoint and returns 0 if ANY endpoint is reachable (a violation), else 1.
forbidden_datastores_reachable() {
  local probe="$1" dest="$2"; shift 2
  local services=("$@") svc port ip found=1
  [[ ${#services[@]} -gt 0 ]] || services=(postgres redis minio)
  for svc in "${services[@]}"; do
    port="$(datastore_port "$svc")"
    if ns_tcp "$probe" "$svc" "$port"; then printf '%s by name %s:%s\n' "$svc" "$svc" "$port"; found=0; fi
    for ip in $(container_ips "$(cid_of "$dest" "$svc")"); do
      if ns_tcp "$probe" "$ip" "$port"; then printf '%s by IP %s:%s\n' "$svc" "$ip" "$port"; found=0; fi
    done
  done
  return "$found"
}

trials_boundaries() { # trials_boundaries DEST INTELLIGENCE(yes|no)
  local dest="$1" intel="$2" ok=1 web core ic ip svc hits
  web="$(cid_of "$dest" web)"; core="$(cid_of "$dest" core)"
  if hits="$(forbidden_datastores_reachable "$web" "$dest")"; then ok=0; log "web reaches datastores: ${hits//$'\n'/; }"; fi
  for svc in postgres redis minio; do
    ns_resolves "$web" "$svc" && { ok=0; log "web resolves $svc"; }
  done
  if [[ "$intel" == yes ]]; then
    ic="$(cid_of "$dest" intelligence)"
    ns_tcp "$web" intelligence 8000 && { ok=0; log "web reaches intelligence"; }
    for ip in $(container_ips "$ic"); do ns_tcp "$web" "$ip" 8000 && { ok=0; log "web reaches intelligence by IP"; }; done
    if hits="$(forbidden_datastores_reachable "$ic" "$dest")"; then ok=0; log "intelligence reaches datastores: ${hits//$'\n'/; }"; fi
    docker inspect -f '{{range .Config.Env}}{{println .}}{{end}}' "$ic" | cut -d= -f1 | grep -qE '^(DB_|REDIS_|OBJECT_STORE_|MINIO_|POSTGRES_)' && { ok=0; log "intelligence env holds data-store settings"; }
    ns_tcp "$core" intelligence 8000 || { ok=0; log "core cannot reach intelligence"; }
    trials_execute_probes "$dest" "$core" || ok=0
  fi
  # Positive control: core (on the data network) does reach every datastore,
  # so the probe can detect reachability at all.
  for svc in postgres redis minio; do
    forbidden_datastores_reachable "$core" "$dest" "$svc" >/dev/null || { ok=0; log "control: core cannot reach $svc (probe ineffective)"; }
  done
  local label; label="T-BOUNDARIES-$(basename "$dest" | cut -d- -f2)"
  if [[ $ok == 1 ]]; then
    if [[ $intel == yes ]]; then
      record "$label" PASS "web isolated from data stores and intelligence; intelligence isolated from data stores (each by name and by IP on its own port) and holds no data credentials; core reaches every datastore (positive control) and intelligence; execute endpoint 401/422/typed stream"
    else
      record "$label" PASS "web isolated from data stores (each by name and by IP on its own port); core reaches every datastore (positive control)"
    fi
  else
    record "$label" FAIL "boundary violation"
  fi
}

# Negative controls: the boundary check must FAIL whenever any single
# datastore is reachable on its own.
# (1) Real reachability: a probe container attached to the data network must
#     be flagged for postgres alone, redis alone and minio alone.
# (2) Independence: with a stubbed probe that reaches ONLY one datastore IP
#     on its own port, the full multi-datastore check must still flag it.
trials_boundary_controls() { # trials_boundary_controls DEST
  local dest="$1" ok=1 net probe svc hits
  net="$(docker inspect -f '{{range $k, $v := .NetworkSettings.Networks}}{{$k}} {{end}}' "$(cid_of "$dest" postgres)" | tr ' ' '\n' | grep -E '_data$' | head -1)"
  probe="$(docker run -d --rm --network "$net" "$BUSYBOX_IMAGE" sleep 300)"
  for svc in postgres redis minio; do
    if hits="$(forbidden_datastores_reachable "$probe" "$dest" "$svc")"; then
      log "control: $svc reachable from the data network is flagged (${hits//$'\n'/; })"
    else ok=0; log "control FAILED: $svc reachable but not flagged"; fi
  done
  docker rm -f "$probe" >/dev/null 2>&1 || true
  local isolated; isolated="$(cid_of "$dest" web)"
  for svc in postgres redis minio; do
    if hits="$(
      STUB_SVC_IPS=" $(container_ips "$(cid_of "$dest" "$svc")")" STUB_PORT="$(datastore_port "$svc")"
      ns_tcp() { [[ "$3" == "$STUB_PORT" && "$STUB_SVC_IPS " == *" $2 "* ]]; }
      forbidden_datastores_reachable "$isolated" "$dest"
    )"; then
      grep -q "^$svc by IP" <<<"$hits" || { ok=0; log "control FAILED: stubbed $svc-only reachability attributed wrongly: $hits"; }
    else ok=0; log "control FAILED: stubbed $svc-only reachability not flagged"; fi
  done
  [[ $ok == 1 ]] && record T-BOUNDARIES-CONTROLS PASS "negative controls: postgres-only, redis-only and minio-only reachability each fail the boundary check (real data-network probe and stubbed single-endpoint probe)" || record T-BOUNDARIES-CONTROLS FAIL "a negative control was not detected"
}

trials_execute_probes() { # From core's network namespace, using busybox wget.
  local dest="$1" core="$2" token out ok=0
  token="$(env_get "$dest" INTELLIGENCE_SERVICE_TOKEN)"
  local url="http://intelligence:8000/internal/v1/execute"
  out="$(docker run --rm --network "container:$core" "$BUSYBOX_IMAGE" wget -S -q -O- --header 'Content-Type: application/json' --post-data '{}' "$url" 2>&1 || true)"
  grep -q ' 401 ' <<<"$out" || { log "execute without token: $(head -1 <<<"$out")"; return 1; }
  out="$(docker run --rm --network "container:$core" -e T="$token" "$BUSYBOX_IMAGE" sh -c 'wget -S -q -O- --header "Authorization: Bearer $T" --header "Content-Type: application/json" --post-data "{\"bad\":true}" '"$url"' 2>&1' || true)"
  grep -q ' 422 ' <<<"$out" || { log "execute with bad envelope: $(head -1 <<<"$out")"; return 1; }
  local body; body="$(valid_envelope_json)"
  out="$(docker run --rm --network "container:$core" -e T="$token" -e B="$body" "$BUSYBOX_IMAGE" sh -c 'wget -q -O- --header "Authorization: Bearer $T" --header "Content-Type: application/json" --post-data "$B" '"$url"' 2>&1' || true)"
  if grep -q '"type": *"error"' <<<"$out" && grep -q 'capability_not_configured' <<<"$out" && grep -q '"type": *"done"' <<<"$out"; then ok=1; fi
  [[ $ok == 1 ]] || { log "execute with valid envelope returned: $out"; return 1; }
}

trials_outage() { # trials_outage DEST CORE_PORT WEB_PORT INTELLIGENCE(yes|no)
  local dest="$1" core="$2" web="$3" intel="$4" ok=1 svc
  for svc in redis postgres minio; do
    compose "$dest" stop "$svc" >/dev/null 2>&1
    local seen=0 i res
    for ((i = 0; i < 30; i++)); do
      res="$(http_get 127.0.0.1 "$core" /readyz)"
      if [[ "$(cut -f1 <<<"$res")" == 503 ]] && grep -q "\"$( [[ $svc == minio ]] && echo object_store || echo "$svc")\":\"unavailable\"" <<<"$res"; then seen=1; break; fi
      sleep 1
    done
    [[ $seen == 1 ]] || { ok=0; log "core /readyz did not report $svc outage"; }
    [[ "$(http_get 127.0.0.1 "$core" /healthz | cut -f1)" == 200 ]] || { ok=0; log "liveness failed during $svc outage"; }
    compose "$dest" start "$svc" >/dev/null 2>&1
    wait_http 127.0.0.1 "$core" /readyz 200 90 || { ok=0; log "core did not recover after $svc"; }
  done
  if [[ "$intel" == yes ]]; then
    compose "$dest" stop intelligence >/dev/null 2>&1; sleep 2
    local res; res="$(http_get 127.0.0.1 "$core" /readyz)"
    [[ "$(cut -f1 <<<"$res")" == 200 ]] && grep -q '"intelligence":"unavailable"' <<<"$res" || { ok=0; log "intelligence outage: $res"; }
    compose "$dest" start intelligence >/dev/null 2>&1
  fi
  compose "$dest" stop core >/dev/null 2>&1
  wait_http 127.0.0.1 "$web" /api/ready 503 30 || { ok=0; log "web /api/ready did not report core outage"; }
  compose "$dest" start core >/dev/null 2>&1
  compose "$dest" up -d --wait >/dev/null 2>&1 || { ok=0; log "stack did not recover"; }
  [[ $ok == 1 ]] && record T-OUTAGE PASS "redis/postgres/minio outages -> core 503 naming the dependency, liveness 200, recovery 200; intelligence outage non-blocking; core outage -> web /api/ready 503" || record T-OUTAGE FAIL "outage behavior"
}

trials_persist() { # trials_persist DEST CORE_PORT
  local dest="$1" ok=1 db
  db="$(env_get "$dest" DATABASE_NAME)"
  compose "$dest" exec -T postgres psql -q -U postgres -d "$db" -c "CREATE TABLE public.archetype_marker (v text); INSERT INTO public.archetype_marker VALUES ('kept');" >/dev/null || ok=0
  compose "$dest" exec -T minio sh -c 'true' >/dev/null 2>&1 || true
  compose "$dest" exec -T -e MC_CONFIG_DIR=/tmp/mc minio /usr/bin/mc alias set local http://127.0.0.1:9000 "$(env_get "$dest" MINIO_ROOT_USER)" "$(env_get "$dest" MINIO_ROOT_PASSWORD)" >/dev/null || ok=0
  printf 'kept' | compose "$dest" exec -T -e MC_CONFIG_DIR=/tmp/mc minio /usr/bin/mc pipe "local/$(env_get "$dest" OBJECT_STORE_BUCKET)/archetype-marker.txt" >/dev/null || ok=0
  compose "$dest" exec -T redis sh -c 'REDISCLI_AUTH="$REDIS_PASSWORD" redis-cli SET archetype-marker kept' >/dev/null || ok=0
  run_docs_block "$dest" stop "$WORK/persist-stop.log" || ok=0
  run_docs_block "$dest" start "$WORK/persist-start.log" || ok=0
  [[ "$(compose "$dest" exec -T postgres psql -tA -U postgres -d "$db" -c 'SELECT v FROM public.archetype_marker')" == kept ]] || { ok=0; log "postgres marker lost"; }
  compose "$dest" exec -T -e MC_CONFIG_DIR=/tmp/mc minio /usr/bin/mc alias set local http://127.0.0.1:9000 "$(env_get "$dest" MINIO_ROOT_USER)" "$(env_get "$dest" MINIO_ROOT_PASSWORD)" >/dev/null || ok=0
  [[ "$(compose "$dest" exec -T -e MC_CONFIG_DIR=/tmp/mc minio /usr/bin/mc cat "local/$(env_get "$dest" OBJECT_STORE_BUCKET)/archetype-marker.txt" 2>/dev/null)" == kept ]] || { ok=0; log "minio marker lost"; }
  [[ -z "$(compose "$dest" exec -T redis sh -c 'REDISCLI_AUTH="$REDIS_PASSWORD" redis-cli GET archetype-marker' | tr -d '\r\n')" ]] || { ok=0; log "redis kept data across restart"; }
  [[ $ok == 1 ]] && record T-PERSIST PASS "postgres table and MinIO object survive down/up; Redis key does not (ephemeral)" || record T-PERSIST FAIL "persistence behavior"
}

trials_rename() { # trials_rename DEST CORE_PORT WEB_PORT
  local dest="$1" core="$2" web="$3" ok=1 before after res
  before="$(compose "$dest" images -q | LC_ALL=C sort | tr '\n' ' ')"
  env_set "$dest" PRODUCT_NAME '"Renamed Example"'
  env_set "$dest" PLATFORM_BASE_DOMAIN renamed.example.net
  env_set "$dest" CUSTOM_DOMAIN_CNAME_TARGET custom.renamed.example.net
  compose "$dest" up -d --no-build --wait >"$WORK/rename1.log" 2>&1 || { ok=0; tail -20 "$WORK/rename1.log"; }
  after="$(compose "$dest" images -q | LC_ALL=C sort | tr '\n' ' ')"
  [[ "$before" == "$after" ]] || { ok=0; log "images changed during rename"; }
  res="$(http_get 127.0.0.1 "$web" /)"
  grep -q '<title>Renamed Example</title>' <<<"$res" && grep -q 'renamed.example.net' <<<"$res" || { ok=0; log "web did not show renamed identity"; }
  grep -q '"product_name":"Renamed Example"' <<<"$(http_get 127.0.0.1 "$core" /readyz)" || { ok=0; log "core did not show renamed identity"; }
  env_set "$dest" DATABASE_NAME renamed_db
  compose "$dest" down -v >/dev/null 2>&1
  compose "$dest" up -d --no-build --wait >"$WORK/rename2.log" 2>&1 || { ok=0; tail -20 "$WORK/rename2.log"; }
  res="$(compose "$dest" exec -T postgres psql -tA -U postgres -d renamed_db -c "SELECT string_agg(rolname, ',' ORDER BY rolname) FROM pg_roles WHERE rolname LIKE 'renamed_db_%'")"
  [[ "$res" == "renamed_db_migrator,renamed_db_owner,renamed_db_retention,renamed_db_runtime" ]] || { ok=0; log "roles after DATABASE_NAME change: $res"; }
  wait_http 127.0.0.1 "$core" /readyz 200 60 || { ok=0; log "core not ready after database rename"; }
  [[ $ok == 1 ]] && record T-RENAME PASS "rename + re-domain via .env only (no rebuild: image IDs unchanged); DATABASE_NAME change yields new database and <db>_* roles; no re-render or search/replace" || record T-RENAME FAIL "rename behavior"
}

trials_toggle() { # trials_toggle BRAVO_DEST
  local dest="$1" ok=1
  compose "$dest" config --services | grep -q intelligence && ok=0
  compose "$dest" config --images | grep -qi python && ok=0
  grep -qE 'AI_MODEL_|OPENROUTER_API_KEY|INTELLIGENCE_' "$dest/.env.example" "$dest/infra/compose/local.yml" && ok=0
  [[ -d "$dest/intelligence" ]] && ok=0
  docker ps --filter "label=com.docker.compose.project=$(env_get "$dest" COMPOSE_PROJECT_NAME)" --format '{{.Image}}' | grep -qi python && ok=0
  [[ $ok == 1 ]] && record T-TOGGLE PASS "bravo: no intelligence service, Python image, AI configuration or intelligence directory; stack healthy without them" || record T-TOGGLE FAIL "intelligence-disabled path not clean"
}

# Minimal valid execution envelope for the runtime probe (no capability configured).
valid_envelope_json() {
  printf '%s' '{"request_id":"req-probe-0001","trace_id":"4bf92f3577b34da6a3ce929d0e0e4736","capability":"chat","authorization_ref":"authz-probe","organization_id":"a0000000-0000-4000-8000-000000000001","user_id":"c0000000-0000-4000-8000-000000000001","allowed_tools":[],"deadline":"2999-01-01T00:00:00Z","context_refs":[],"input":{"messages":[{"role":"user","content":"probe"}]}}'
}

# ===================================================================================
final_checks() {
  # T-DOCKER-ONLY: no shadowed host runtime was invoked during the whole run.
  local abs
  abs="$(grep -nE '(^|[^A-Za-z0-9_./-])/usr/(local/)?bin/(go|python3?|node|npm|npx)\b' "$VALIDATION"/*.sh "$SKILL/generator/render.sh" || true)"
  if [[ -s "$SHADOW_LOG" ]]; then record T-DOCKER-ONLY FAIL "host runtime invoked: $(head -3 "$SHADOW_LOG" | tr '\n' ';')"
  elif [[ -n "$abs" ]]; then record T-DOCKER-ONLY FAIL "absolute host runtime path in scripts: $abs"
  else record T-DOCKER-ONLY PASS "no host Go/Python/Node/psql/redis-cli/mc/aws invocation (shadowed, /mnt/* stripped); no absolute runtime paths"; fi
  # Required images: those referenced by the rendered projects plus the harness tools.
  {
    printf '%s\n' "$GO_IMAGE" "$YQ_IMAGE" "$BUSYBOX_IMAGE" "$SHELLCHECK_IMAGE" "$HADOLINT_IMAGE" "$GITLEAKS_IMAGE"
    if [[ -n "${V_ALPHA:-}" ]]; then
      grep -h '^FROM ' "$V_ALPHA"/infra/docker/*.Dockerfile | awk '{print $2}' | grep -v '^[a-z-]*$' || true
      grep -h 'image: ' "$V_ALPHA/infra/compose/local.yml" | awk '{print $2}' || true
    fi
  } | grep -v '^$' | LC_ALL=C sort -u >"$WORK/images.txt"
  record IMAGES INFO "$(tr '\n' ' ' <"$WORK/images.txt")"

  # T-SECRET-CANARY: generated secrets never appear in logs, manifests or rendered files (except .env).
  local leaks=0 s files=()
  mapfile -t files < <(find "$WORK" -type f ! -name '.env' ! -name 'secrets' ! -name 'env.backup' ! -path '*/shadow-bin/*' ! -name '*.dump' 2>/dev/null)
  if [[ -s "$SECRETS_FILE" && ${#files[@]} -gt 0 ]]; then
    while IFS= read -r s; do
      [[ -n "$s" ]] || continue
      if grep -lF -- "$s" "${files[@]}" 2>/dev/null | grep -q .; then leaks=1; log "a generated secret appears in: $(grep -lF -- "$s" "${files[@]}" | head -3 | tr '\n' ' ')"; fi
    done <"$SECRETS_FILE"
  fi
  [[ $leaks == 0 ]] && record T-SECRET-CANARY PASS "$(wc -l <"$SECRETS_FILE") generated secrets absent from harness output, compose logs, manifests and rendered files" || record T-SECRET-CANARY FAIL "secret leaked"

  # T-REPO-CLEAN
  if [[ -n "$REPO_ROOT" ]]; then
    local bad
    bad="$(git -C "$REPO_ROOT" status --porcelain --untracked-files=all | awk '{print $2}' | grep -vE -- "$REPO_ALLOW" || true)"
    local roots=""; for r in web core intelligence contracts infra; do [[ -e "$REPO_ROOT/$r" ]] && roots="$roots $r"; done
    if [[ -z "$bad" && -z "$roots" ]]; then record T-REPO-CLEAN PASS "repository changes limited to --repo-allow paths; no root web/core/intelligence/contracts/infra"
    else record T-REPO-CLEAN FAIL "unexpected: ${bad//$'\n'/ } ${roots}"; fi
  fi
}

for s in "${SUITES[@]}"; do "suite_$s"; done
final_checks

log "== summary"
LC_ALL=C sort -t$'\t' -k1,1 "$RESULTS" | awk -F'\t' '{printf "%-28s %-6s %s\n", $1, $2, $3}'
if [[ -n "$REPORT_DIR" ]]; then mkdir -p "$REPORT_DIR"; cp "$RESULTS" "$REPORT_DIR/results.tsv"; cp "$WORK/images.txt" "$REPORT_DIR/" 2>/dev/null || true; fi
if grep -qE $'\t(FAIL|UNMET)\t' "$RESULTS"; then log "RESULT: FAILED"; exit 1; fi
log "RESULT: PASSED"
