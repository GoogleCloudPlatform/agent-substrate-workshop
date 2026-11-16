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

# delete-cluster.sh: delete the GKE cluster in workshop.env (CLUSTER_NAME, CLUSTER_LOCATION) without leaving its
# disks behind. Deleting a cluster doesn't delete the persistent disks behind its PersistentVolumeClaims, such as
# the 500 GiB volume of Substrate's bundled Postgres, so this deletes every claim first and waits for GKE to delete
# their disks. Then it deletes the cluster and lists any pvc- disks still in the project.
# Run make teardown (ALL=1) first for the rest of the workshop's resources. Set YES=1 to skip the prompt.
source "$(dirname "$0")/lib.sh"
load_env
require_cluster_location
if [[ "${YES:-}" != 1 ]]; then
  echo "This deletes the GKE cluster $CLUSTER_NAME ($CLUSTER_LOCATION) in project $PROJECT_ID, everything running in it,"
  echo "and the disks behind its PersistentVolumeClaims. It can't be undone."
  read -r -p "Type the cluster name to continue: " answer || answer=""
  [[ "$answer" == "$CLUSTER_NAME" ]] || die "aborted"
fi
stop_port_forwards

step "persistent volumes in $CLUSTER_NAME"
# GKE deletes a provisioned disk when its PersistentVolume goes, for reclaim policy Delete (the default).
deletable_pvs() {
  kubectl get pv -o jsonpath='{range .items[?(@.spec.persistentVolumeReclaimPolicy=="Delete")]}{.metadata.name}{"\n"}{end}'
}
no_deletable_pvs() { local pvs; pvs="$(deletable_pvs)" || return 1; [[ -z "$pvs" ]]; }
if gcloud container clusters get-credentials "$CLUSTER_NAME" --location "$CLUSTER_LOCATION" --project "$PROJECT_ID" >/dev/null 2>&1; then
  # A StatefulSet keeps its claims, and a claim stays while a Pod uses it, so stop Postgres first.
  kubectl -n ate-system delete statefulset postgres --ignore-not-found --cascade=foreground --wait=true >/dev/null ||
    warn "could not delete statefulset ate-system/postgres"
  kubectl delete pvc --all --all-namespaces --wait=false >/dev/null || warn "could not delete every PersistentVolumeClaim"
  if ( wait_for 300 "PersistentVolumes to be released" no_deletable_pvs ); then
    ok "PersistentVolumes released"
  else
    warn "some PersistentVolumes are still there; check the disk list at the end"
  fi
else
  warn "could not get credentials for $CLUSTER_NAME; deleting it without releasing its disks first"
fi

step "cluster $CLUSTER_NAME ($CLUSTER_LOCATION)"
gcloud container clusters delete "$CLUSTER_NAME" --location "$CLUSTER_LOCATION" --project "$PROJECT_ID" --quiet
ok "cluster deleted"

step "pvc- disks left in $PROJECT_ID"
disks="$(gcloud compute disks list --project "$PROJECT_ID" --filter='name~^pvc-' --format='table(name,sizeGb,zone.basename())' 2>/dev/null)" || disks=""
if [[ -n "$disks" ]]; then
  warn "these disks still exist and keep billing (some may belong to other clusters in this project):"
  echo "$disks" >&2
  echo "    delete one with: gcloud compute disks delete NAME --zone ZONE --project $PROJECT_ID" >&2
else
  ok "no pvc- disks left"
fi
