#!/usr/bin/env bash
# ko-builds a workshop binary into your Artifact Registry and records its digest-pinned ref.
# ActorTemplates reject image refs without a digest, so we always keep the @sha256 form.
source "$(dirname "$0")/lib.sh"
load_env
name="${1:-}"
case "$name" in agent|gateway) ;; *) die "usage: build.sh agent|gateway";; esac
export KO_DOCKER_REPO
step "ko build $name -> $KO_DOCKER_REPO"
ref="$(cd "$REPO_ROOT/app" && ko build --base-import-paths "./cmd/$name")"
[[ "$ref" == *@sha256:* ]] || die "ko did not return a digest-pinned ref: $ref"
state_put "$name-image" "$ref"
ok "$ref  (saved to .workshop/$name-image)"
