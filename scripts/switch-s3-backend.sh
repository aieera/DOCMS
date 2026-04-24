#!/usr/bin/env bash
# Switch .env between MinIO (local) and AWS (staging/prod) templates.
# Either copies the template verbatim (default) or symlinks it. The
# copy path is the default because editing an .env that's actually a
# symlink to an .example accidentally commits credentials to the
# .example when the user pushes.
#
# Usage:
#   bash scripts/switch-s3-backend.sh local    # from .env.local.example
#   bash scripts/switch-s3-backend.sh aws      # from .env.aws.example
#   bash scripts/switch-s3-backend.sh --link local    # symlink instead of copy

set -euo pipefail

LINK=0
if [[ "${1:-}" == "--link" ]]; then
    LINK=1
    shift
fi

MODE="${1:-}"
case "$MODE" in
    local) SRC=".env.local.example" ;;
    aws)   SRC=".env.aws.example" ;;
    *)
        echo "usage: $0 [--link] {local|aws}" >&2
        echo "  local — MinIO via docker-compose" >&2
        echo "  aws   — AWS S3 (leave keys blank; IRSA provides them in k8s)" >&2
        exit 2
        ;;
esac

if [[ ! -f "$SRC" ]]; then
    echo "source missing: $SRC — run from repo root" >&2
    exit 1
fi

if [[ -f .env && ! -L .env ]]; then
    # A real file already exists — refuse to clobber so operators
    # don't lose hand-edited secrets. They can rm or mv it first.
    echo ".env exists and is not a symlink; refusing to overwrite." >&2
    echo "mv .env .env.backup && rerun." >&2
    exit 1
fi

rm -f .env
if [[ "$LINK" -eq 1 ]]; then
    ln -s "$SRC" .env
    echo "linked .env -> $SRC"
else
    cp "$SRC" .env
    echo "copied $SRC -> .env (edit to add real credentials)"
fi
