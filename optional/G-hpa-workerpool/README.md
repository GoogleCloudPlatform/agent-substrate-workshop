# Optional lab G — Autoscale the WorkerPool (outline)

**Goal:** grow and shrink the number of workers with demand, so idle-suspend (Module 5) and capacity work
together instead of a fixed `replicas: 2`.

**You'll need:** the Module 6 end state (Managed Prometheus and gateway metrics).

1. **Confirmed, not just "check":** the WorkerPool CRD already wires a `/scale` subresource —
   `manifests/ate-install/generated/ate.dev_workerpools.yaml` in the substrate checkout has
   `subresources.scale` with `specReplicasPath: .spec.replicas`, `statusReplicasPath: .status.replicas`,
   `labelSelectorPath: .status.selector`. That means a standard Kubernetes `HorizontalPodAutoscaler` can
   target it directly:
   ```bash
   kubectl get crd workerpools.ate.dev -o yaml | grep -A3 subresources   # confirms it on your cluster too
   ```
2. Pick a signal: parked requests or router `no_free_worker`/timeout outcomes (substrate's
   `atenet.router.route.duration_count` by `ate.router.outcome`, from `observability/queries.md`), or
   `workshop_actors_awake` against `{"ate.workerpool.desired_workers"}` (gateway + substrate, the same ratio
   this lab's dashboard tile in [optional lab D](../D-observability-deep-dive/README.md) charts).
3. Install the Custom Metrics Stackdriver Adapter, or use KEDA with a Prometheus scaler against the GMP
   frontend (`make prometheus-ui`'s target, `svc/prometheus-ui`, is a real Prometheus-compatible endpoint a
   scaler can query in-cluster).
4. Create an HPA:
   ```yaml
   apiVersion: autoscaling/v2
   kind: HorizontalPodAutoscaler
   metadata: {name: agents, namespace: workshop}
   spec:
     scaleTargetRef: {apiVersion: ate.dev/v1alpha1, kind: WorkerPool, name: agents}
     minReplicas: 2
     maxReplicas: 4
     metrics: [...]   # your chosen signal from step 2
   ```
   (or a KEDA `ScaledObject` with the same `scaleTargetRef`). Run `CONCURRENCY=4 make load`.
5. Watch workers grow on the dashboard (`ate.workerpool.workers` by state), and shrink back after load
   stops. Discuss: node-pool capacity has to keep up with `maxReplicas` (Module 5's oversubscription lesson
   applies to nodes too), and every new worker still starts from the template's golden snapshot, so scaling
   out doesn't change the golden-snapshot warm-start story from Module 2.

Status: outline — contributions welcome.
