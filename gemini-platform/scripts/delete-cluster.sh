#!/usr/bin/env bash
# Copyright 2026 Google LLC
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

# delete-cluster.sh: release all PersistentVolumeClaims (including Substrate's bundled Postgres PVC)
# before deleting the GKE cluster so persistent disks are not orphaned in the project.
source "$(dirname "$0")/lib.sh"
load_env
require_cluster_location

if [[ "${YES:-}" != 1 ]]; then
  echo "This deletes GKE cluster $CLUSTER_NAME ($CLUSTER_LOCATION) in project $PROJECT_ID and its PersistentVolumeClaims."
  read -r -p "Type the cluster name to continue: " answer || answer=""
  [[ "$answer" == "$CLUSTER_NAME" ]] || die "aborted"
fi
stop_port_forwards

step "PersistentVolumes in $CLUSTER_NAME"
deletable_pvs() {
  kubectl get pv -o jsonpath='{range .items[?(@.spec.persistentVolumeReclaimPolicy=="Delete")]}{.metadata.name}{"\n"}{end}'
}
no_deletable_pvs() { local pvs; pvs="$(deletable_pvs)" || return 1; [[ -z "$pvs" ]]; }

if gcloud container clusters get-credentials "$CLUSTER_NAME" --location "$CLUSTER_LOCATION" --project "$PROJECT_ID" >/dev/null 2>&1; then
  kubectl -n "$NAMESPACE" delete deploy sre-gateway --ignore-not-found --wait=true >/dev/null 2>&1 || true
  kubectl -n ate-system delete statefulset postgres --ignore-not-found --cascade=foreground --wait=true >/dev/null || \
    warn "could not delete statefulset ate-system/postgres"
  kubectl delete pvc --all --all-namespaces --wait=false >/dev/null || warn "could not delete every PersistentVolumeClaim"
  if ( wait_for 300 "PersistentVolumes to be released" no_deletable_pvs ); then
    ok "PersistentVolumes released"
  else
    warn "some PersistentVolumes remain; inspect the disk list below"
  fi
else
  warn "could not fetch credentials for $CLUSTER_NAME; proceeding with cluster deletion"
fi

step "Delete cluster $CLUSTER_NAME ($CLUSTER_LOCATION)"
gcloud container clusters delete "$CLUSTER_NAME" --location "$CLUSTER_LOCATION" --project "$PROJECT_ID" --quiet
ok "cluster deleted"

step "Check remaining pvc- disks in $PROJECT_ID"
disks="$(gcloud compute disks list --project "$PROJECT_ID" --filter='name~^pvc-' --format='table(name,sizeGb,zone.basename())' 2>/dev/null)" || disks=""
if [[ -n "$disks" ]]; then
  warn "remaining pvc- disks in project $PROJECT_ID:"
  echo "$disks" >&2
else
  ok "no pvc- disks left"
fi
