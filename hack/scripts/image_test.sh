#!/bin/sh
#
# Copyright 2026 Luca Burgazzoli
#
# Licensed under the Apache License, Version 2.0 (the "License");
# you may not use this file except in compliance with the License.
# You may obtain a copy of the License at
#
#     http://www.apache.org/licenses/LICENSE-2.0
#
# Unless required by applicable law or agreed to in writing, software
# distributed under the License is distributed on an "AS IS" BASIS,
# WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
# See the License for the specific language governing permissions and
# limitations under the License.

# Dependency-free POSIX contract tests for hack/scripts/image.sh.
#
# A task-owned fake podman is placed first on a controlled PATH; it
# records every invocation and simulates enough local image storage
# for manifest/image existence checks. No container daemon, network,
# registry, or Go toolchain is needed. Run from any directory:
#
#   sh hack/scripts/image_test.sh
#
# Note on Make forwarding tests: GNU Make expands "$" in command-line
# variable assignments, so a literal dollar sign must be written "$$"
# on the make command line. The tests below document that expansion
# where they use it.
set -u

script_dir=$(CDPATH='' cd -- "$(dirname -- "$0")" && pwd)
repo_root=$(CDPATH='' cd -- "$script_dir/../.." && pwd)
image_sh="$repo_root/hack/scripts/image.sh"

tmp=$(mktemp -d "${TMPDIR:-/tmp}/image-test.XXXXXX") || exit 1
# Canonicalize (mktemp may leave a double slash when TMPDIR ends in
# "/"), so string comparisons against pwd-normalized paths hold.
tmp=$(CDPATH='' cd -- "$tmp" && pwd)
cleanup() {
  rm -rf "$tmp"
}
trap cleanup EXIT INT TERM

fake_bin="$tmp/bin"
fake_home="$tmp/fake-podman"
log="$fake_home/invocations.log"
store="$fake_home/store"
mkdir -p "$fake_bin" "$store"
: > "$log"

# ------------------------------------------------------------------
# Fake podman: records argv and simulates a tiny local image store.
# ------------------------------------------------------------------
cat > "$fake_bin/podman" <<'EOF'
#!/bin/sh
set -u
home=${FAKE_PODMAN_HOME:?}
log="$home/invocations.log"
store="$home/store"

printf 'BEGIN %s\n' "$#" >> "$log"
i=1
for arg in "$@"; do
  printf 'ARG %s %s\n' "$i" "$arg" >> "$log"
  i=$((i + 1))
done

sanitize() {
  printf '%s' "$1" | tr '/:' '__'
}

command=$1
case $command in
  build)
    if [ -n "${FAKE_PODMAN_FAIL_BUILD:-}" ]; then
      exit 7
    fi
    ref=
    manifest=
    prev=
    for arg in "$@"; do
      if [ "$prev" = "--tag" ]; then ref=$arg; fi
      if [ "$prev" = "--manifest" ]; then manifest=$arg; fi
      prev=$arg
    done
    if [ -n "$manifest" ]; then
      printf 'list' > "$store/$(sanitize "$manifest").type"
    elif [ -n "$ref" ]; then
      printf 'image' > "$store/$(sanitize "$ref").type"
    fi
    exit 0
    ;;
  manifest)
    case $2 in
      exists)
        f="$store/$(sanitize "$3").type"
        if [ -f "$f" ] && [ "$(cat "$f")" = "list" ]; then
          exit 0
        fi
        exit 1
        ;;
      rm)
        rm -f "$store/$(sanitize "$3").type"
        exit 0
        ;;
    esac
    exit 0
    ;;
  image)
    case $2 in
      exists)
        if [ -f "$store/$(sanitize "$3").type" ]; then
          exit 0
        fi
        exit 1
        ;;
    esac
    exit 0
    ;;
  tag)
    src="$store/$(sanitize "$2").type"
    if [ -f "$src" ]; then
      cp "$src" "$store/$(sanitize "$3").type"
    fi
    exit 0
    ;;
  rmi)
    rm -f "$store/$(sanitize "$2").type"
    exit 0
    ;;
esac
exit 0
EOF
chmod +x "$fake_bin/podman"

# Sentinel command: if any layer under test ever evaluates the
# adversarial values as shell program text, this runs and leaves a
# marker the tests then detect.
cat > "$fake_bin/image-test-sentinel" <<'EOF'
#!/bin/sh
printf 'executed\n' >> "${FAKE_PODMAN_HOME:?}/sentinel-executed"
EOF
chmod +x "$fake_bin/image-test-sentinel"

export FAKE_PODMAN_HOME="$fake_home"

# ------------------------------------------------------------------
# Harness
# ------------------------------------------------------------------
tests=0
failures=0

assert_eq() {
  # assert_eq NAME EXPECTED ACTUAL
  tests=$((tests + 1))
  if [ "$2" = "$3" ]; then
    printf 'ok   %s\n' "$1"
  else
    failures=$((failures + 1))
    printf 'FAIL %s\n     expected: [%s]\n     actual:   [%s]\n' "$1" "$2" "$3"
  fi
}

assert_contains() {
  # assert_contains NAME NEEDLE HAYSTACK
  tests=$((tests + 1))
  case $3 in
    *"$2"*)
      printf 'ok   %s\n' "$1"
      ;;
    *)
      failures=$((failures + 1))
      printf 'FAIL %s\n     expected to contain: [%s]\n     actual:            [%s]\n' \
        "$1" "$2" "$3"
      ;;
  esac
}

assert_file_absent() {
  tests=$((tests + 1))
  if [ ! -e "$2" ]; then
    printf 'ok   %s\n' "$1"
  else
    failures=$((failures + 1))
    printf 'FAIL %s: file exists: %s\n' "$1" "$2"
  fi
}

invocations() {
  grep -c '^BEGIN ' "$log" || true
}

inv_args() {
  # inv_args N: print one argument of invocation N per line
  awk -v want="$1" '
    /^BEGIN / { inv++; next }
    inv == want && /^ARG / {
      line = $0
      sub(/^ARG [0-9]+ /, "", line)
      print line
    }
  ' "$log"
}

inv_arg() {
  # inv_arg N J: print argument J of invocation N
  inv_args "$1" | sed -n "$2p"
}

reset_engine() {
  : > "$log"
  rm -f "$store"/*.type
  rm -f "$fake_home/sentinel-executed"
  # A FAKE_PODMAN_FAIL_BUILD=1 prefix on a run_script call persists
  # after the function under POSIX sh; always clear it here.
  unset FAKE_PODMAN_FAIL_BUILD
}

reset_log() {
  # Clear recorded invocations but keep the simulated image store,
  # so repeat-run tests observe state left by earlier runs.
  : > "$log"
  rm -f "$fake_home/sentinel-executed"
  unset FAKE_PODMAN_FAIL_BUILD
}

store_type() {
  # store_type REF: print "list", "image", or nothing
  f="$store/$(printf '%s' "$1" | tr '/:' '__').type"
  if [ -f "$f" ]; then
    cat "$f"
  fi
}

err_file="$tmp/stderr.txt"

run_script() {
  # run_script [PATH_VALUE --] ARGS...: run image.sh with a fake PATH
  if [ "${1:-}" = "--path" ]; then
    path_value=$2
    shift 2
  else
    path_value="$fake_bin:$PATH"
  fi
  PATH="$path_value" "$image_sh" "$@" 2>"$err_file"
  run_rc=$?
}

# ------------------------------------------------------------------
# Syntax
# ------------------------------------------------------------------
sh -n "$image_sh"
assert_eq "image.sh passes sh -n" 0 "$?"

# ------------------------------------------------------------------
# build: defaults, overrides, literal forwarding
# ------------------------------------------------------------------
reset_engine
run_script build
assert_eq "build default: exit status" 0 "$run_rc"
assert_eq "build default: engine invocations" 1 "$(invocations)"
assert_eq "build default: argv" "build
--file
$repo_root/Containerfile
--tag
quay.io/lburgazzoli/odh-platform-tools:dev
$repo_root" "$(inv_args 1)"

reset_engine
run_script build --registry example.com --name mytool --tag v1
assert_eq "build overrides: exit status" 0 "$run_rc"
assert_eq "build overrides: tag argument" "example.com/mytool:v1" "$(inv_arg 1 5)"

reset_engine
evil_tag='a b`image-test-sentinel`$(pwd);'"'"'q"d-e'
run_script build --tag "$evil_tag"
assert_eq "build adversarial value: exit status" 0 "$run_rc"
assert_eq "build adversarial value: delivered literally" \
  "quay.io/lburgazzoli/odh-platform-tools:$evil_tag" "$(inv_arg 1 5)"
assert_file_absent "build adversarial value: no sentinel execution" \
  "$fake_home/sentinel-executed"

# ------------------------------------------------------------------
# Repository-root resolution, including a path with spaces
# ------------------------------------------------------------------
spaced_root="$tmp/repo with spaces"
mkdir -p "$spaced_root/hack/scripts"
cp "$image_sh" "$spaced_root/hack/scripts/image.sh"
: > "$spaced_root/Containerfile"

reset_engine
(
  cd "$tmp" || exit 1
  PATH="$fake_bin:$PATH" "$spaced_root/hack/scripts/image.sh" build 2>"$err_file"
)
assert_eq "spaced repo root: exit status" 0 "$?"
assert_eq "spaced repo root: context argument" "$spaced_root" "$(inv_arg 1 6)"
assert_eq "spaced repo root: Containerfile argument" \
  "$spaced_root/Containerfile" "$(inv_arg 1 3)"

# ------------------------------------------------------------------
# multiarch: platform set, repeatability, reference safety
# ------------------------------------------------------------------
reset_engine
run_script multiarch
assert_eq "multiarch first run: exit status" 0 "$run_rc"
assert_eq "multiarch first run: engine invocations" 6 "$(invocations)"
assert_eq "multiarch first run: command" "build" "$(inv_arg 3 1)"
assert_eq "multiarch first run: platform 1" "linux/amd64" "$(inv_arg 3 3)"
assert_eq "multiarch first run: platform 2" "linux/arm64" "$(inv_arg 3 5)"
assert_eq "multiarch first run: platform count" 2 \
  "$(inv_args 3 | grep -c '^linux/' || true)"
first_temp=$(inv_arg 3 7)
case $first_temp in
  localhost/odh-platform-tools:multiarch-tmp-*) tmp_ok=yes ;;
  *) tmp_ok=no ;;
esac
assert_eq "multiarch first run: private temp index name" yes "$tmp_ok"
assert_eq "multiarch first run: moves list into requested reference" \
  "tag
$first_temp
quay.io/lburgazzoli/odh-platform-tools:dev" "$(inv_args 5)"
assert_eq "multiarch first run: reference is a list" "list" \
  "$(store_type quay.io/lburgazzoli/odh-platform-tools:dev)"
assert_eq "multiarch first run: temp index cleaned up" "" "$(store_type "$first_temp")"

reset_log
run_script multiarch
assert_eq "multiarch repeated run: exit status" 0 "$run_rc"
# The existing list makes "manifest exists" succeed, so the run skips
# the image probe: invocation 1 is the probe, invocation 2 the build.
assert_eq "multiarch repeated run: existing list skips image probe" \
  "manifest
exists
quay.io/lburgazzoli/odh-platform-tools:dev" "$(inv_args 1)"
assert_eq "multiarch repeated run: command" "build" "$(inv_arg 2 1)"
assert_eq "multiarch repeated run: platform 1" "linux/amd64" "$(inv_arg 2 3)"
assert_eq "multiarch repeated run: platform 2" "linux/arm64" "$(inv_arg 2 5)"
assert_eq "multiarch repeated run: platform count" 2 \
  "$(inv_args 2 | grep -c '^linux/' || true)"
second_temp=$(inv_arg 2 7)
if [ "$first_temp" != "$second_temp" ]; then temps_differ=yes; else temps_differ=no; fi
assert_eq "multiarch repeated run: fresh temp index" yes "$temps_differ"
assert_eq "multiarch repeated run: reference is a list" "list" \
  "$(store_type quay.io/lburgazzoli/odh-platform-tools:dev)"
assert_eq "multiarch repeated run: temp index cleaned up" "" "$(store_type "$second_temp")"

reset_engine
run_script build --tag clash
assert_eq "incompatible reference setup: native build exit status" 0 "$run_rc"
run_script multiarch --tag clash
assert_eq "incompatible reference: exit status" 1 "$run_rc"
assert_contains "incompatible reference: diagnostic names the reference" \
  "quay.io/lburgazzoli/odh-platform-tools:clash" "$(cat "$err_file")"
assert_contains "incompatible reference: diagnostic advises a distinct tag" \
  "distinct tag" "$(cat "$err_file")"
# Invocation 1 is the setup build; only existence probes may follow.
assert_eq "incompatible reference: only existence probes, no build" 3 "$(invocations)"
assert_eq "incompatible reference: probes reference with image exists" "image
exists
quay.io/lburgazzoli/odh-platform-tools:clash" "$(inv_args 3)"
assert_eq "incompatible reference: existing image preserved" "image" \
  "$(store_type quay.io/lburgazzoli/odh-platform-tools:clash)"

# ------------------------------------------------------------------
# Usage errors: exit 2, no engine call
# ------------------------------------------------------------------
check_usage_error() {
  # check_usage_error NAME ARGS...
  name=$1
  shift
  reset_engine
  run_script "$@"
  assert_eq "$name: exit status" 2 "$run_rc"
  assert_contains "$name: usage on stderr" "usage:" "$(cat "$err_file")"
  assert_eq "$name: engine not called" 0 "$(invocations)"
}

check_usage_error "no command"
check_usage_error "unknown command" frobnicate
check_usage_error "push is rejected" push
check_usage_error "push is rejected even with options" push --tag x
check_usage_error "unknown option" build --bogus
check_usage_error "missing value at end" build --tag
check_usage_error "empty value" build --tag ''
check_usage_error "option passed as value" build --tag --name
check_usage_error "unexpected positional argument" build extra

# ------------------------------------------------------------------
# Absent Podman
# ------------------------------------------------------------------
# Build a sterile PATH instead of assuming a system directory lacks
# Podman: on Linux, /usr/bin/podman is a normal installation, and the
# suite must stay portable to such hosts. The sterile directory holds
# symlinks only to the external utilities image.sh uses before it
# requires Podman; shell builtins (cd, pwd, command, printf in most
# shells) need no entry, and command -v reports builtins without a
# path, which the case below skips.
sterile_bin="$tmp/sterile-bin"
mkdir -p "$sterile_bin"
for util in dirname cat printf; do
  util_path=$(command -v "$util" 2>/dev/null) || util_path=
  case $util_path in
    /*) ln -s "$util_path" "$sterile_bin/$util" ;;
  esac
done

if PATH="$sterile_bin" command -v podman >/dev/null 2>&1; then
  tests=$((tests + 1))
  failures=$((failures + 1))
  printf 'FAIL absent podman: a podman binary exists in %s; test environment not sterile\n' \
    "$sterile_bin"
else
  run_script --path "$sterile_bin" build
  assert_eq "absent podman: exit status" 1 "$run_rc"
  assert_contains "absent podman: actionable diagnostic" "podman" "$(cat "$err_file")"
fi

# ------------------------------------------------------------------
# Engine failure propagation
# ------------------------------------------------------------------
reset_engine
FAKE_PODMAN_FAIL_BUILD=1 run_script build
assert_eq "engine failure: exit status propagates" 7 "$run_rc"

# ------------------------------------------------------------------
# Make forwarding through both targets
# ------------------------------------------------------------------
run_make() {
  # run_make TARGET [ASSIGNMENTS...]
  target=$1
  shift
  PATH="$fake_bin:$PATH" make -C "$repo_root" "$target" "$@" 2>"$err_file"
  run_rc=$?
}

reset_engine
run_make image
assert_eq "make image default: exit status" 0 "$run_rc"
assert_eq "make image default: engine invocations" 1 "$(invocations)"
assert_eq "make image default: argv" "build
--file
$repo_root/Containerfile
--tag
quay.io/lburgazzoli/odh-platform-tools:dev
$repo_root" "$(inv_args 1)"

reset_engine
run_make image IMAGE_REGISTRY=example.com IMAGE_NAME=mytool IMAGE_TAG=v1
assert_eq "make image overrides: reference" "example.com/mytool:v1" "$(inv_arg 1 5)"

reset_engine
run_make image IMAGE_TAG='two words'
assert_eq "make image spaces: value delivered as one argument" \
  "quay.io/lburgazzoli/odh-platform-tools:two words" "$(inv_arg 1 5)"

reset_engine
run_make image IMAGE_TAG='a`image-test-sentinel`b'
assert_eq "make image backticks: exit status" 0 "$run_rc"
assert_eq "make image backticks: delivered literally" \
  'quay.io/lburgazzoli/odh-platform-tools:a`image-test-sentinel`b' "$(inv_arg 1 5)"
assert_file_absent "make image backticks: no sentinel execution" \
  "$fake_home/sentinel-executed"

reset_engine
run_make image IMAGE_NAME='q"d'"'"'e'
assert_eq "make image quotes: delivered literally" \
  'quay.io/lburgazzoli/q"d'"'"'e:dev' "$(inv_arg 1 5)"

reset_engine
run_make image IMAGE_TAG='a$$b'
assert_eq 'make image dollar (written $$, make expands to one $): delivered' \
  'quay.io/lburgazzoli/odh-platform-tools:a$b' "$(inv_arg 1 5)"
assert_file_absent "make image dollar: no sentinel side effects" \
  "$fake_home/sentinel-executed"

reset_engine
run_make image IMAGE_TAG='x;touch /tmp/never-created-9x7q'
assert_eq "make image semicolon: delivered literally" \
  'quay.io/lburgazzoli/odh-platform-tools:x;touch /tmp/never-created-9x7q' \
  "$(inv_arg 1 5)"
assert_file_absent "make image semicolon: no command executed" /tmp/never-created-9x7q

reset_engine
run_make image/multiarch IMAGE_TAG=make-multi
assert_eq "make image/multiarch: exit status" 0 "$run_rc"
assert_eq "make image/multiarch: platforms" "linux/amd64
linux/arm64" "$(inv_args 3 | grep '^linux/' || true)"
assert_eq "make image/multiarch: reference is a list" "list" \
  "$(store_type quay.io/lburgazzoli/odh-platform-tools:make-multi)"

# ------------------------------------------------------------------
# Make targets stay thin
# ------------------------------------------------------------------
dry_run_image=$( (cd "$repo_root" && make -n image) 2>/dev/null)
assert_eq "make -n image: single delegation line" \
  './hack/scripts/image.sh build --registry "$IMAGE_REGISTRY" --name "$IMAGE_NAME" --tag "$IMAGE_TAG"' \
  "$dry_run_image"

dry_run_multi=$( (cd "$repo_root" && make -n image/multiarch) 2>/dev/null)
assert_eq "make -n image/multiarch: single delegation line" \
  './hack/scripts/image.sh multiarch --registry "$IMAGE_REGISTRY" --name "$IMAGE_NAME" --tag "$IMAGE_TAG"' \
  "$dry_run_multi"

make_help=$( (cd "$repo_root" && make help) 2>/dev/null)
assert_contains "make help lists image" "image" "$make_help"
assert_contains "make help lists image/multiarch" "image/multiarch" "$make_help"

# ------------------------------------------------------------------
# Summary
# ------------------------------------------------------------------
printf '\n%d checks, %d failures\n' "$tests" "$failures"
if [ "$failures" -gt 0 ]; then
  exit 1
fi
exit 0
