# Optional lab F — Autoscale the WorkerPool (30 min)

> **Cluster impact:** Cluster-wide, with a clean-up. Claims the cluster's only `external.metrics.k8s.io` APIService and puts an HPA on `agents`. Run the clean-up before [lab G](../G-agent-swarm/README.md).

**Goal:** grow and shrink the number of workers with demand, so idle-suspend (Module 5) and capacity work
together instead of a fixed `replicas: 2`.

**You'll need:** the Module 5 end state (the swarm, `IDLE_AFTER=3s`). This lab brings its own Prometheus,
so it doesn't depend on Module 6's Managed Prometheus, but Module 6's dashboard is where you'll watch it.

This is the same autoscaler [optional lab G](../G-agent-swarm/README.md)'s agent-swarm runs for its
`conf-workers` pool (`manifests/autoscaling.yaml.tmpl` there), pointed at your `agents` pool.

> **Only one adapter can serve `external.metrics.k8s.io` per cluster.** This lab registers its
> prometheus-adapter there, and so does lab G's `hack/deploy.sh up` (it refuses to run if another adapter
> holds it). Do one lab at a time: run the other lab's clean-up first.

## 1. The WorkerPool is already scalable

The WorkerPool CRD wires a `/scale` subresource, so a standard `HorizontalPodAutoscaler` can target it
directly, like a Deployment:
```bash
kubectl get crd workerpools.ate.dev -o yaml | grep -A3 subresources
kubectl -n workshop get workerpool agents -o jsonpath='{.status}{"\n"}'   # replicas, readyReplicas, selector
```

## 2. The signal: worker demand

CPU is the wrong signal for a WorkerPool. A worker's CPU says nothing about the requests *waiting* for a
worker, and those are what more workers would fix. Substrate exports both halves of demand:

- `ate.workerpool.workers` by `ate.worker.state` (ateapi): how many workers host at least one actor
  (`partial` or `at_capacity`). A busy workshop worker shows as `partial`, not `at_capacity`: the state counts
  actor slots (1 of ateom's default 1000), not CPU, so count both.
- `atenet.router.parking.active` (atenet-router): requests parked at the router waiting for a worker
  (Module 5's 60s parking budget). Each one needs a seat; a workshop worker has 1 seat (1 CPU limit, 1 CPU
  per actor).

So: **demand (in workers) = busy workers + ceil(parked requests / seats per worker)**. The HPA's target is
1 per replica, so it asks for exactly that many workers, between 2 and 4.

## 3. Install Prometheus, prometheus-adapter and the HPA

`autoscaling.yaml.tmpl` deploys a small Prometheus that scrapes `atenet-router` and `ate-api-server`
every 5s (Managed Prometheus works too, but its ingestion delay makes the HPA a minute late), the
prometheus-adapter with one external rule (`workshop_worker_demand`, the formula above), and the HPA. The
APIService is a separate file and goes on last: an APIService with no ready backend breaks API discovery for
the whole cluster.
```bash
scripts/render.sh optional/F-hpa-workerpool/autoscaling.yaml.tmpl | kubectl apply -f -
kubectl -n workshop rollout status deploy/workshop-prometheus deploy/workshop-prometheus-adapter --timeout=180s
scripts/render.sh optional/F-hpa-workerpool/autoscaling-apiservice.yaml.tmpl | kubectl apply -f -
kubectl get apiservice v1beta1.external.metrics.k8s.io        # AVAILABLE True after ~30s
kubectl get --raw /apis/external.metrics.k8s.io/v1beta1/namespaces/workshop/workshop_worker_demand | jq '.items[].value'
kubectl -n workshop get hpa agents                           # TARGETS n/1, not <unknown>
```

## 4. Load it
```bash
kubectl -n workshop get hpa agents -w &     # stop it with `kill %1` when you're done
CONCURRENCY=4 make load
```
Four chats at a time against two workers: two actors get workers, the other two park at the router, and
demand jumps to 4. The pool grows within about 15–30s of the burst, and every new worker starts from the
template's golden snapshot, so scaling out doesn't change the warm-start story from Module 2. Compare the
load's `==> status codes` summary with Module 6's run on a fixed 2 workers.

Watch the same thing on the dashboard: `ate.workerpool.workers` by state (Module 6), and
`{"ate.workerpool.desired_workers", "ate.workerpool.name"="agents"}`. When the load stops, the gateway
idle-suspends every actor within 3s, demand drops to 0, and the pool shrinks back to 2 after the HPA's
2-minute scale-down window.

## What to notice
- **Idle-suspend and autoscaling are two halves of one loop.** Idle-suspend frees seats on the workers you
  have; the HPA adds workers when seats run out. Without idle-suspend (`IDLE_AFTER=0s`), every actor you ever
  talked to holds a seat, demand never falls, and the pool sits at `maxReplicas`.
- **Node capacity has to keep up with `maxReplicas`.** Module 5's oversubscription lesson applies to nodes
  too: 4 workers × 250m CPU / 1Gi requests fit on the workshop's two nodes; a bigger `maxReplicas` needs
  cluster autoscaling or a bigger node pool, or new workers sit `Pending`.
- **The router is shared.** `atenet.router.parking.active` has no atespace label, so parked requests for other
  pools (lab A's `ate-env`, lab G's `conf-workers`) count as demand here too.

## Clean up
Delete the APIService first, for the same discovery reason as above:
```bash
scripts/render.sh optional/F-hpa-workerpool/autoscaling-apiservice.yaml.tmpl | kubectl delete -f - --ignore-not-found
scripts/render.sh optional/F-hpa-workerpool/autoscaling.yaml.tmpl | kubectl delete -f - --ignore-not-found
kubectl -n workshop patch workerpool agents --type merge -p '{"spec":{"replicas":2}}'
```

## Next
Back to [Module 7 — Wrap-up](../../modules/07-wrap-up/README.md) for the rest of the optional labs.
