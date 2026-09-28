# Optional lab B — microVM sandbox class (outline)

**Goal:** run the same agent under substrate's microVM sandbox class (`ateom-microvm`: a Kata guest on Cloud
Hypervisor) instead of gVisor, and compare isolation, cold start, and snapshot size.

**You'll need:** a GKE node pool with nested virtualization enabled (exposes `/dev/kvm`), and the microVM
asset chain from the substrate checkout at `SUBSTRATE_REF` — this is opt-in and more involved than gVisor,
which is installed unconditionally.

1. Create (or add) a node pool with nested virtualization. `tools/setup-gcp` in the substrate checkout
   creates one with `--enable-nested-virtualization` (on by default there); doing it by hand means a
   machine type that supports nested virt plus the `ate.dev/substrate-version` label
   `install-substrate.sh` already puts on every node.
2. Stage the microVM assets (cloud-hypervisor, virtiofsd, guest kernel, guest rootfs) to your bucket and
   apply the cluster-wide `SandboxConfig`, per `.workshop/substrate/hack/install-microvm-deps.sh --install`
   and the comments at the top of `.workshop/substrate/manifests/microvm/sandboxconfig-microvm.yaml.tmpl`.
   This is opt-in — unlike `sandboxconfig-gvisor.yaml`, nothing applies it for you.
3. Add a second WorkerPool (`agents-vm`) that selects the nested-virt node pool, and create `agent-vm-v1`: a
   copy of `agent-v3` with `sandboxConfig: {sandboxClass: SANDBOX_CLASS_MICROVM, configName: microvm}` — see
   `.workshop/substrate/demos/counter/counter-microvm-template.yaml.tmpl` for a working reference.
4. Create an actor on `agent-vm-v1`, then compare restore P95 and snapshot size against gVisor's `agent-v3`
   on the workshop dashboard (`ate.actor.restore.duration` and `atelet.snapshot.size` both carry `ate.template.name`,
   and restore duration also carries `ate.sandbox.class`, per `docs/metrics/registry/metrics.yaml` in the substrate
   checkout, so the existing dashboard tiles should show both side by side without editing them).
5. Discuss: when is the stronger VM boundary worth the extra cold-start and setup cost?

For local (non-GKE) experimentation with the same runtime, `.workshop/substrate/docs/dev/microvm-local.md`
covers a kind-cluster path with Lima on Apple Silicon.

Status: outline — contributions welcome.
