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

"""Idempotently patches agent-substrate/env (.workshop/env) for Agent Substrate v1.0.0-rc.2 compatibility."""

import os
import subprocess
import sys


def patch_imports(env_dir: str) -> None:
    for root, _, files in os.walk(env_dir):
        if ".git" in root.split(os.sep):
            continue
        for name in files:
            if not name.endswith(".go"):
                continue
            path = os.path.join(root, name)
            with open(path, "r", encoding="utf-8") as fh:
                src = fh.read()
            updated = src.replace(
                '\t"go.ate.dev/substrate/pkg/proto/ateapipb"',
                '\tateapipb "go.ate.dev/substrate/api/ateapi/v1"',
            ).replace(
                '\tatev1alpha1 "go.ate.dev/substrate/pkg/api/v1alpha1"',
                '\tatev1alpha1 "go.ate.dev/substrate/api/infra/k8s/ate/v1"',
            ).replace(
                "Status: ateapipb.SnapshotStorageStatus_SNAPSHOT_STORAGE_STATUS_COMPLETED,",
                "State:  ateapipb.SnapshotStorageState_SNAPSHOT_STORAGE_STATE_COMPLETED,",
            )
            if updated != src:
                with open(path, "w", encoding="utf-8") as fh:
                    fh.write(updated)


def patch_manifest_go(env_dir: str) -> None:
    path = os.path.join(env_dir, "cmd", "ate-env", "manifest.go")
    with open(path, "r", encoding="utf-8") as fh:
        content = fh.read()

    # v1.0.0-rc.1+ rejects snapshotConfig.storageLocation unless PerTemplateSnapshotLocation preview gate is enabled.
    content = content.replace("\t\t\tStorageLocation: cfg.snapshotsBucket,\n", "")

    old_resources = """\t\tResources: &ateapipb.Resources{Limits: []*ateapipb.Limits{
\t\t\t{Name: "cpu", Quantity: orDefault(cfg.cpu, defaultActorCPU)},
\t\t\t{Name: "memory", Quantity: orDefault(cfg.memory, defaultActorMemory)},
\t\t}},
\t}
}"""
    new_resources = """\t\tResources: &ateapipb.ResourceRequirements{Limits: buildLimits(
\t\t\torDefault(cfg.cpu, defaultActorCPU),
\t\t\torDefault(cfg.memory, defaultActorMemory),
\t\t)},
\t}
}

func buildLimits(cpu, memory string) *ateapipb.Resources {
\tcpuQ := resource.MustParse(cpu)
\tmemQ := resource.MustParse(memory)
\treturn &ateapipb.Resources{
\t\tCpuMillicores: cpuQ.MilliValue(),
\t\tMemoryBytes:   memQ.Value(),
\t}
}

func formatLimits(l *ateapipb.Resources) map[string]string {
\tif l == nil {
\t\treturn nil
\t}
\treturn map[string]string{
\t\t"cpu":    resource.NewMilliQuantity(l.GetCpuMillicores(), resource.DecimalSI).String(),
\t\t"memory": resource.NewQuantity(l.GetMemoryBytes(), resource.BinarySI).String(),
\t}
}"""
    content = content.replace(old_resources, new_resources)

    with open(path, "w", encoding="utf-8") as fh:
        fh.write(content)


def patch_manifest_test_go(env_dir: str) -> None:
    path = os.path.join(env_dir, "cmd", "ate-env", "manifest_test.go")
    with open(path, "r", encoding="utf-8") as fh:
        content = fh.read()

    old_limits = """func limits(tmpl *ateapipb.ActorTemplate) map[string]string {
\tm := map[string]string{}
\tfor _, l := range tmpl.GetResources().GetLimits() {
\t\tm[l.GetName()] = l.GetQuantity()
\t}
\treturn m
}"""
    new_limits = """func limits(tmpl *ateapipb.ActorTemplate) map[string]string {
\treturn formatLimits(tmpl.GetResources().GetLimits())
}"""
    content = content.replace(old_limits, new_limits)

    content = content.replace(
        '[]string{"name: cpu\\n    quantity: \\"4\\"", "name: memory\\n    quantity: 8Gi"}',
        '[]string{"cpuMillicores: \\"4000\\"", "memoryBytes: \\"8589934592\\""}',
    )
    old_storage_assert = """\tif template.GetSnapshotConfig().GetStorageLocation() != cfg.snapshotsBucket {
\t\tt.Errorf("snapshots location = %q, want %q", template.GetSnapshotConfig().GetStorageLocation(), cfg.snapshotsBucket)
\t}
"""
    content = content.replace(old_storage_assert, "")
    content = content.replace('\t\t"storageLocation: gs://bucket/ate-env/",\n', "")
    content = content.replace(
        '!strings.Contains(out, "image: example.com/guest@sha256:aaaa") || !strings.Contains(out, "storageLocation: gs://bucket/prefix/")',
        '!strings.Contains(out, "image: example.com/guest@sha256:aaaa")',
    )

    with open(path, "w", encoding="utf-8") as fh:
        fh.write(content)


def patch_imagetemplate_test_go(env_dir: str) -> None:
    path = os.path.join(env_dir, "internal", "apiservice", "imagetemplate_test.go")
    with open(path, "r", encoding="utf-8") as fh:
        content = fh.read()

    old_res = """\t\tResources: &ateapipb.Resources{Limits: []*ateapipb.Limits{
\t\t\t{Name: "cpu", Quantity: "2"}, {Name: "memory", Quantity: "4Gi"},
\t\t}},"""
    new_res = """\t\tResources: &ateapipb.ResourceRequirements{Limits: &ateapipb.Resources{
\t\t\tCpuMillicores: 2000, MemoryBytes: 4 << 30,
\t\t}},"""
    content = content.replace(old_res, new_res)

    with open(path, "w", encoding="utf-8") as fh:
        fh.write(content)


def patch_go_mod(env_dir: str) -> None:
    env = os.environ.copy()
    env["GOWORK"] = "off"
    subprocess.run(
        ["go", "get", "go.ate.dev/substrate@v1.0.0-rc.2"],
        cwd=env_dir,
        env=env,
        check=True,
    )
    subprocess.run(
        ["go", "mod", "tidy"],
        cwd=env_dir,
        env=env,
        check=True,
    )


def main() -> None:
    env_dir = sys.argv[1] if len(sys.argv) > 1 else "."
    patch_imports(env_dir)
    patch_manifest_go(env_dir)
    patch_manifest_test_go(env_dir)
    patch_imagetemplate_test_go(env_dir)
    patch_go_mod(env_dir)


if __name__ == "__main__":
    main()
