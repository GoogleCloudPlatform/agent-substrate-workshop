#!/usr/bin/env python3
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

"""Idempotently patches google/ax (.workshop/ax) for Agent Substrate v0.4.0 compatibility."""

import os
import subprocess
import sys


def patch_go_mod(ax_dir: str) -> None:
    env = os.environ.copy()
    env["GOWORK"] = "off"
    subprocess.run(
        ["go", "get", "github.com/agent-substrate/substrate@v0.4.0"],
        cwd=ax_dir,
        env=env,
        check=True,
    )
    subprocess.run(
        ["go", "mod", "tidy"],
        cwd=ax_dir,
        env=env,
        check=True,
    )


def patch_substrate_client(ax_dir: str) -> None:
    path = os.path.join(ax_dir, "internal", "substrate", "client.go")
    with open(path, "r", encoding="utf-8") as fh:
        content = fh.read()

    # 1. Use /ko-app/ax-task-runner so `ko build --base-import-paths ./cmd/ax-task-runner` works out of the box.
    content = content.replace(
        'DefaultGuestCommand    = "/usr/local/bin/ax-task-runner"',
        'DefaultGuestCommand    = "/ko-app/ax-task-runner"',
    )

    # 2. v0.2.0+ renamed ContainerReadyz -> ContainerWakeupProbe.
    #    Probe /healthz so AteomHerder/Run finishes as soon as the runner's HTTP server is up (which
    #    transitions the actor to RUNNING and activates its EgressPolicy), while TaskReconciler polls
    #    /readyz?check=workspace until SetupWorkspace finishes cloning Git repos.
    old_readyz = """\t\t\tReadyz: &ateapipb.ContainerReadyz{
\t\t\t\tHttpGet: &ateapipb.HTTPGetAction{
\t\t\t\t\tPath: "/readyz",
\t\t\t\t\tPort: 80,
\t\t\t\t},
\t\t\t},"""
    new_wakeup = """\t\t\tWakeupProbe: &ateapipb.ContainerWakeupProbe{
\t\t\t\tHttpGet: &ateapipb.HTTPGetAction{
\t\t\t\t\tPath: "/healthz",
\t\t\t\t\tPort: 80,
\t\t\t\t},
\t\t\t\tTimeoutSeconds: 30,
\t\t\t},"""
    content = content.replace(old_readyz, new_wakeup)
    content = content.replace(new_wakeup.replace('"/healthz"', '"/readyz"'), new_wakeup)

    # 3. v0.2.0+ renamed SnapshotsConfig -> SnapshotConfig; v0.4.0 removed OnPause and OnResume.
    #    Also select the lab's `workload: ax-worker` WorkerPool so AX tasks don't land on `agents`.
    old_snapshots = """\t\tSnapshotsConfig: &ateapipb.SnapshotsConfig{
\t\t\tStorageLocation: snapshotsBucket,
\t\t\tOnPause:         ateapipb.SnapshotContentScope_SNAPSHOT_CONTENT_SCOPE_DATA,
\t\t\tOnCommit:        ateapipb.SnapshotContentScope_SNAPSHOT_CONTENT_SCOPE_DATA,
\t\t\tOnResume: &ateapipb.OnResumeConfig{
\t\t\t\tFromData: ateapipb.ResumeSource_RESUME_SOURCE_GOLDEN,
\t\t\t},
\t\t},"""
    new_snapshots = """\t\tSnapshotConfig: &ateapipb.SnapshotConfig{
\t\t\tStorageLocation: snapshotsBucket,
\t\t\tOnCommit:        ateapipb.SnapshotContentScope_SNAPSHOT_CONTENT_SCOPE_DATA,
\t\t},
\t\tWorkerSelector: &ateapipb.Selector{
\t\t\tMatchLabels: map[string]string{
\t\t\t\t"workload": "ax-worker",
\t\t\t},
\t\t},"""
    content = content.replace(old_snapshots, new_snapshots)

    # 4. Wait for the new template's golden snapshot before CreateActor (v0.4.0 returns FailedPrecondition
    #    until status.goldenSnapshotStatus.goldenTag is set).
    old_create = "\tactor, err := c.control.CreateActor(ctx, req)\n\tif err != nil {"
    new_create = """\tactor, err := c.control.CreateActor(ctx, req)
\tfor retry := 0; err != nil && status.Code(err) == codes.FailedPrecondition && retry < 30; retry++ {
\t\tselect {
\t\tcase <-ctx.Done():
\t\t\treturn nil, ctx.Err()
\t\tcase <-time.After(2 * time.Second):
\t\t}
\t\tactor, err = c.control.CreateActor(ctx, req)
\t}
\tif err != nil {"""
    content = content.replace(old_create, new_create)

    # 5. v0.4.0 replaced WorkerPodIp string with WorkerPodIps []string.
    old_worker_ip = """\tif resp.Actor.Status != nil && resp.Actor.Status.WorkerAssignment != nil {
\t\tworkerIP = resp.Actor.Status.WorkerAssignment.WorkerPodIp
\t}"""
    new_worker_ip = """\tif resp.Actor.Status != nil && resp.Actor.Status.WorkerAssignment != nil && len(resp.Actor.Status.WorkerAssignment.WorkerPodIps) > 0 {
\t\tworkerIP = resp.Actor.Status.WorkerAssignment.WorkerPodIps[0]
\t}"""
    content = content.replace(old_worker_ip, new_worker_ip)

    with open(path, "w", encoding="utf-8") as fh:
        fh.write(content)


def patch_reconciler_test(ax_dir: str) -> None:
    path = os.path.join(ax_dir, "internal", "controller", "reconciler_test.go")
    with open(path, "r", encoding="utf-8") as fh:
        content = fh.read()
    content = content.replace("WorkerPodIp: wIP,", "WorkerPodIps: []string{wIP},")
    with open(path, "w", encoding="utf-8") as fh:
        fh.write(content)


def patch_workspace_setup(ax_dir: str) -> None:
    # BuildActorTemplate uses SNAPSHOT_CONTENT_SCOPE_DATA with durableDir at /workspace, so
    # keep the maiden-run marker inside /workspace/.ax so it survives suspend/resume.
    path = os.path.join(ax_dir, "internal", "workspace", "setup.go")
    with open(path, "r", encoding="utf-8") as fh:
        content = fh.read()
    content = content.replace('DefaultAXDir = "/ax"', 'DefaultAXDir = "/workspace/.ax"')
    with open(path, "w", encoding="utf-8") as fh:
        fh.write(content)


def patch_ax_cli_timeout(ax_dir: str) -> None:
    # Give `ax apply` 120s so it doesn't time out while Substrate takes the template's golden snapshot (~20s).
    path = os.path.join(ax_dir, "cmd", "ax", "main.go")
    with open(path, "r", encoding="utf-8") as fh:
        content = fh.read()
    content = content.replace("30*time.Second", "120*time.Second")
    content = content.replace("30 * time.Second", "120 * time.Second")
    with open(path, "w", encoding="utf-8") as fh:
        fh.write(content)


def patch_deploy_manifest(ax_dir: str) -> None:
    # Omit the cluster-wide Secret ClusterRole and ClusterRoleBinding from deploy/ax-server.yaml
    # so the lab stays strictly namespace-scoped to ax-system.
    path = os.path.join(ax_dir, "deploy", "ax-server.yaml")
    with open(path, "r", encoding="utf-8") as fh:
        content = fh.read()
    rbac_docs = """---
apiVersion: rbac.authorization.k8s.io/v1
kind: ClusterRole
metadata:
  name: ax-server
rules:
  - apiGroups: [""]
    resources: ["secrets"]
    verbs: ["get", "list", "watch"]
---
apiVersion: rbac.authorization.k8s.io/v1
kind: ClusterRoleBinding
metadata:
  name: ax-server
subjects:
  - kind: ServiceAccount
    name: ax-server
    namespace: ax-system
roleRef:
  apiGroup: rbac.authorization.k8s.io
  kind: ClusterRole
  name: ax-server
"""
    content = content.replace(rbac_docs, "")
    with open(path, "w", encoding="utf-8") as fh:
        fh.write(content)


def main() -> int:
    ax_dir = sys.argv[1] if len(sys.argv) > 1 else ".workshop/ax"
    ax_dir = os.path.abspath(ax_dir)
    if not os.path.isdir(ax_dir):
        print(f"error: AX directory not found: {ax_dir}", file=sys.stderr)
        return 1

    patch_go_mod(ax_dir)
    patch_substrate_client(ax_dir)
    patch_reconciler_test(ax_dir)
    patch_workspace_setup(ax_dir)
    patch_ax_cli_timeout(ax_dir)
    patch_deploy_manifest(ax_dir)
    print(f"Patched AX at {ax_dir} for Agent Substrate v0.4.0.")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
