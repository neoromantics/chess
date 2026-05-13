#!/usr/bin/env bash
#
# Bootstrap chess-secrets on the k3s cluster. Run ONCE on the VM after
# cluster creation, or whenever you want to rotate everything from
# scratch (note: rotating Postgres creds requires also wiping the
# chess-db PVC, since Postgres only honors POSTGRES_USER/PASSWORD on
# first init of the data directory).
#
# Idempotent: re-running with --force replaces an existing Secret.
# Without --force it errors out so you don't accidentally overwrite
# the live secret.
#
# Usage:
#   ./infra/bootstrap-secrets.sh             # create, refuse to overwrite
#   ./infra/bootstrap-secrets.sh --force     # replace existing
#
set -euo pipefail

NAMESPACE="${NAMESPACE:-chess}"
FORCE=0
[[ "${1:-}" == "--force" ]] && FORCE=1

if ! command -v kubectl >/dev/null; then
  echo "kubectl not found in PATH" >&2
  exit 1
fi
if ! command -v openssl >/dev/null; then
  echo "openssl not found in PATH" >&2
  exit 1
fi

kubectl create namespace "$NAMESPACE" --dry-run=client -o yaml | kubectl apply -f -

if kubectl -n "$NAMESPACE" get secret chess-secrets >/dev/null 2>&1; then
  if [[ $FORCE -ne 1 ]]; then
    echo "chess-secrets already exists in namespace '$NAMESPACE'." >&2
    echo "Refusing to overwrite without --force." >&2
    exit 1
  fi
  kubectl -n "$NAMESPACE" delete secret chess-secrets
fi

PG_USER="chess_$(openssl rand -hex 4)"
PG_PASS="$(openssl rand -hex 32)"
JWT="$(openssl rand -hex 32)"

kubectl -n "$NAMESPACE" create secret generic chess-secrets \
  --from-literal=POSTGRES_USER="$PG_USER" \
  --from-literal=POSTGRES_PASSWORD="$PG_PASS" \
  --from-literal=POSTGRES_DB=chess \
  --from-literal=JWT_SECRET="$JWT"

echo "Created chess-secrets in namespace '$NAMESPACE'."
echo "POSTGRES_USER=$PG_USER  (Postgres data dir is initialized with this on first PG boot only)"
