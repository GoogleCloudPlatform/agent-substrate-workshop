# Optional lab I — microVM sandbox class (45–60 min)

> **Cluster impact:** Changes the cluster. Adds a nested-virt node pool (one extra `c3-standard-4`, billed until you delete it) and a cluster-wide `SandboxConfig`. **Taint the pool** (step 1): without it, gVisor snapshots taken on that node crash when they restore anywhere else.

**Goal:** run the same agent under substrate's microVM sandbox class (`ateom-microvm`: a Kata guest on Cloud
Hypervisor) instead of gVisor, and compare isolation, cold start, and snapshot size.

**You'll need:** Module 5 done (`agent-v3`, `buddy`, the gateway), and the substrate checkout at
`SUBSTRATE_REF` that `make install` cloned into `.workshop/substrate`. MicroVMs are opt-in and take more
setup than gVisor, which is installed unconditionally: a node pool with `/dev/kvm`, the VM assets in your
bucket, and a worker image built for the microVM class. Run on a GKE cluster on 2026-09-28.

```bash
source scripts/env.sh    # workshop.env into this shell (bash or zsh): PROJECT_ID, BUCKET_NAME, KO_DOCKER_REPO, …
```

1. **A nested-virtualization node pool, tainted.** `--enable-nested-virtualization` exposes `/dev/kvm` to
   the node, and `workshop-pool=microvm` is what step 3's WorkerPool selects:
   ```bash
   gcloud beta container node-pools create microvm --project "$PROJECT_ID" \
     --cluster "$CLUSTER_NAME" --location "$CLUSTER_LOCATION" \
     --machine-type c3-standard-4 --num-nodes 1 --enable-nested-virtualization \
     --node-labels=workshop-pool=microvm \
     --node-taints=ate.dev/sandboxClass=microvm:NoSchedule
   kubectl get nodes -l workshop-pool=microvm      # Ready about 15s after the command returns
   ```
   **Why this taint:** the gVisor WorkerPools (`agents`, lab H's `conf-workers`, lab A's and lab B's pools) have no
   nodeSelector and tolerate only `ate.dev/sandboxClass=gvisor:NoSchedule`, so without this taint
   they land on this node too. A nested-virt CPU exposes the `vmx` feature, so a gVisor snapshot taken there
   fails to restore on an ordinary node (`runsc restore … incompatible FeatureSet: missing features: map[vmx:{}]`)
   and the actor goes CRASHED. Use `ate.dev/sandboxClass=microvm:NoSchedule` (not an arbitrary key): the `atelet`
   DaemonSet tolerates `ate.dev/sandboxClass:NoSchedule`, and without `atelet` on the node the `/dev/kvm` device
   plugin (`ate.dev/kvm`) is never registered and `agents-vm` stays `Pending`.
   On a regional cluster, `--num-nodes` is per zone.
2. **Stage the microVM assets and apply the `SandboxConfig`.** substrate's script downloads
   cloud-hypervisor, virtiofsd, the guest kernel and rootfs, uploads them to `gs://$BUCKET_NAME/kata-assets/`,
   and applies the cluster-wide `SandboxConfig` named `microvm`. Unlike `sandboxconfig-gvisor.yaml`,
   nothing applies it for you:
   ```bash
   (cd .workshop/substrate && GOWORK=off NO_DEV_ENV=1 hack/install-microvm-deps.sh --install)
   kubectl get sandboxconfigs
   ```
   `NO_DEV_ENV=1` makes it use your exported `PROJECT_ID` and `BUCKET_NAME` instead of substrate's own
   `.ate-dev-env.sh`. It picks the asset architecture from `KO_DEFAULTPLATFORMS` (`linux/amd64` by default),
   i.e. your nodes, not your laptop: on an Apple Silicon Mac the host-arch fallback would stage arm64 assets
   for amd64 nodes. The comments at the top of
   `.workshop/substrate/manifests/microvm/sandboxconfig-microvm.yaml.tmpl` explain each field.
3. **A microVM worker pool and template.** The WorkerPool needs substrate's `ateom-microvm` worker image:
   `make install` built only `ateom-gvisor`. Build it the way `scripts/install-substrate.sh` builds the
   gVisor one, then apply [`workerpool.yaml.tmpl`](workerpool.yaml.tmpl) (Module 2's pool plus
   `sandboxClasses: [{name: microvm, configRef: {name: microvm}}]`, the node pool's label, and a toleration for its taint) and
   [`template.yaml.tmpl`](template.yaml.tmpl) (`agent-v3` with
   `sandboxConfig: {sandboxClass: microvm, configName: microvm}`):
   ```bash
   export MICROVM_WORKER_IMAGE="$(cd .workshop/substrate && export GOWORK=off KO_DOCKER_REPO KO_DEFAULTPLATFORMS &&
     ver="$(git describe --tags --always --dirty)" &&
     ./hack/run-tool.sh ko build --base-import-paths "--ldflags=-X=github.com/agent-substrate/substrate/internal/version.Version=$ver" ./cmd/ateom-microvm)"
   scripts/render.sh optional/I-microvm/workerpool.yaml.tmpl | kubectl apply -f -
   kubectl -n workshop get pods -l workload=workshop-agent-vm -o wide     # Running on the microvm node
   scripts/render.sh optional/I-microvm/template.yaml.tmpl | kubectl ate create actor-template -f -
   kubectl ate get actor-template agent-vm-v1 -a workshop -o yaml | grep -A2 goldenTag   # repeat until set (~20s)
   ```
   `diff modules/04-make-it-an-agent/manifests/template.yaml.tmpl optional/I-microvm/template.yaml.tmpl`
   shows everything that changed. `.workshop/substrate/demos/counter/counter-microvm-template.yaml.tmpl` is
   substrate's own reference.
4. Create an actor on `agent-vm-v1`, give it the same egress as `buddy`, and chat with it:
   ```bash
   kubectl ate create actor vm1 --template=agent-vm-v1 -a workshop
   scripts/egress.sh apply modules/04-make-it-an-agent/manifests/egress-gateway.yaml --actor=vm1
   make forward-gateway
   curl -s localhost:8081/chat/vm1 -d '{"message":"Hi, my name is Sam."}'
   sleep 10; curl -s localhost:8081/chat/vm1 -d '{"message":"What is my name?"}'   # after an idle suspend
   ```
   Then compare restore P95 and snapshot size against gVisor's `agent-v3`
   on the workshop dashboard (`ate.actor.restore.duration` and `atelet.snapshot.size` both carry `ate.template.name`,
   and restore duration also carries `ate.sandbox.class`, per `docs/metrics/registry/metrics.yaml` in the substrate
   checkout, so the workshop dashboard's "Actor restore P95 by template" and "Snapshot image size P95 by template" tiles show
   both side by side). Substrate's own "Snapshot Size & QPS" dashboard filters to `file.name="pages.img"`,
   gVisor's memory file, so microVM snapshots (`memory-ranges`) don't appear there.
5. Discuss: when is the stronger VM boundary worth the extra cold-start and setup cost?

For local (non-GKE) experimentation with the same runtime, `.workshop/substrate/docs/dev/microvm-local.md`
covers a kind-cluster path with Lima on Apple Silicon.
