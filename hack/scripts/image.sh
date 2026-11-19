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

# Build and push the odh-platform-tools container image with Podman.
# Images stay in local storage until the push command explicitly
# uploads the composed reference; nothing else ever leaves the machine.
#
# usage: image.sh build     [--registry VALUE] [--name VALUE] [--tag VALUE]
#        image.sh multiarch [--registry VALUE] [--name VALUE] [--tag VALUE]
#        image.sh push      [--registry VALUE] [--name VALUE] [--tag VALUE]
#
# The repository root is resolved from this script's location, so the
# script works from any working directory.
set -eu

SCRIPT_NAME=image.sh
DEFAULT_REGISTRY=quay.io/lburgazzoli
DEFAULT_NAME=odh-platform-tools
DEFAULT_TAG=dev

usage() {
  cat >&2 <<EOF
usage: $SCRIPT_NAME build     [--registry VALUE] [--name VALUE] [--tag VALUE]
       $SCRIPT_NAME multiarch [--registry VALUE] [--name VALUE] [--tag VALUE]
       $SCRIPT_NAME push      [--registry VALUE] [--name VALUE] [--tag VALUE]

Build and push the odh-platform-tools image with Podman. Images stay
local until push is asked for explicitly.

commands:
  build      build the image for the host's native platform
  multiarch  build a local manifest list for linux/amd64 and linux/arm64
  push       push the built image or manifest list at the composed reference

options:
  --registry VALUE   image registry (default: $DEFAULT_REGISTRY)
  --name VALUE       image repository name (default: $DEFAULT_NAME)
  --tag VALUE        image tag (default: $DEFAULT_TAG)
EOF
}

die_usage() {
  usage
  exit 2
}

unknown() {
  printf '%s: %s\n\n' "$SCRIPT_NAME" "$1" >&2
  die_usage
}

# Resolve the repository root from this script's location so direct
# invocation works from any working directory.
script_dir=$(CDPATH='' cd -- "$(dirname -- "$0")" && pwd)
repo_root=$(CDPATH='' cd -- "$script_dir/../.." && pwd)

if [ ! -f "$repo_root/Containerfile" ]; then
  printf '%s: no Containerfile found at %s\n' "$SCRIPT_NAME" "$repo_root" >&2
  exit 1
fi

command_name=
registry=$DEFAULT_REGISTRY
name=$DEFAULT_NAME
tag=$DEFAULT_TAG

if [ "$#" -lt 1 ]; then
  die_usage
fi

command_name=$1
shift

case $command_name in
  build | multiarch | push) ;;
  *) unknown "unknown command: $command_name" ;;
esac

while [ "$#" -gt 0 ]; do
  option=$1
  case $option in
    --registry | --name | --tag)
      if [ "$#" -lt 2 ]; then
        unknown "missing value for $option"
      fi
      value=$2
      case $value in
        '')
          unknown "empty value for $option"
          ;;
        --*)
          # An option name is never a valid value; refusing it keeps a
          # malformed invocation from silently consuming the next
          # option as data.
          unknown "option-like value for $option: $value"
          ;;
      esac
      case $option in
        --registry) registry=$value ;;
        --name) name=$value ;;
        --tag) tag=$value ;;
      esac
      shift 2
      ;;
    --*)
      unknown "unknown option: $option"
      ;;
    *)
      unknown "unexpected argument: $option"
      ;;
  esac
done

# Usage is fully parsed before the engine is required, so argument
# errors never depend on (or invoke) Podman.
podman_bin=$(command -v podman 2>/dev/null || true)
if [ -z "$podman_bin" ]; then
  printf '%s: podman was not found on PATH.\n' "$SCRIPT_NAME" >&2
  printf '%s: install Podman (https://podman.io) and make sure the\n' "$SCRIPT_NAME" >&2
  printf '%s: podman binary is executable, then retry.\n' "$SCRIPT_NAME" >&2
  exit 1
fi

reference="$registry/$name:$tag"

case $command_name in
  build)
    # No --platform: Podman's default is the host's native platform.
    exec "$podman_bin" build \
      --file "$repo_root/Containerfile" \
      --tag "$reference" \
      "$repo_root"
    ;;
  multiarch)
    # A manifest list cannot replace a single-architecture image that
    # already owns the requested reference; refuse rather than delete
    # the existing image.
    if ! "$podman_bin" manifest exists "$reference" >/dev/null 2>&1; then
      if "$podman_bin" image exists "$reference" >/dev/null 2>&1; then
        printf '%s: %s already names a single-architecture image.\n' \
          "$SCRIPT_NAME" "$reference" >&2
        printf '%s: choose a distinct tag for the multiarch index.\n' "$SCRIPT_NAME" >&2
        exit 1
      fi
    fi

    # Build into a private temporary manifest list, then move it to
    # the requested reference, so repeated runs never accumulate
    # duplicate platform entries and unrelated artifacts stay put.
    temp_reference="localhost/${name}:multiarch-tmp-$$"
    "$podman_bin" build \
      --platform linux/amd64 \
      --platform linux/arm64 \
      --manifest "$temp_reference" \
      --file "$repo_root/Containerfile" \
      "$repo_root"

    # The replacement is built; drop a previous multiarch result at
    # the same reference (if any) and move the new list into place.
    # Manifest-list names are removed with "manifest rm": plain "rmi"
    # rejects list names with "tag not known".
    "$podman_bin" manifest rm "$reference" >/dev/null 2>&1 || true
    "$podman_bin" tag "$temp_reference" "$reference"
    "$podman_bin" manifest rm "$temp_reference" >/dev/null 2>&1 || true
    ;;
  push)
    # Push whatever local artifact owns the reference — a manifest
    # list via "manifest push", a single-architecture image via
    # "push" — without building anything: build first with "build" or
    # "multiarch". This is the only command that ever leaves the
    # machine, and it only ever touches the composed reference.
    if "$podman_bin" manifest exists "$reference" >/dev/null 2>&1; then
      exec "$podman_bin" manifest push "$reference"
    fi
    if "$podman_bin" image exists "$reference" >/dev/null 2>&1; then
      exec "$podman_bin" push "$reference"
    fi

    printf '%s: no local image or manifest list at %s.\n' \
      "$SCRIPT_NAME" "$reference" >&2
    printf '%s: build one first with "build" or "multiarch".\n' "$SCRIPT_NAME" >&2
    exit 1
    ;;
esac
