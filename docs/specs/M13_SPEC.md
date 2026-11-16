# M13 Specification: Kubernetes CronJob Deployment Bundle & Asset Verification

## 1. Objective & Target Files

Author the hardened Kubernetes `CronJob` deployment manifests (`deploy/k8s/`) targeting namespace `mealie` and add a deterministic Go contract test validating the production `configs/sources.yaml` and `seeds/greatest-hits.yaml` assets end-to-end.

**Target Files (strict scope):**
1. `deploy/k8s/pvc.yaml`
2. `deploy/k8s/externalsecret.yaml`
3. `deploy/k8s/cronjob.yaml`
4. `internal/config/starter_assets_test.go`

---

## 2. Kubernetes Manifest Contracts (`deploy/k8s/`)

All manifests target namespace `mealie` and conform to `paulojauregui/k8s-gitops` conventions:

1. **`deploy/k8s/pvc.yaml`:**
   - `PersistentVolumeClaim` named `mealie-feeder-state` in namespace `mealie`.
   - `accessModes: ["ReadWriteOnce"]`, `storageClassName: openebs-hostpath`, `resources.requests.storage: 1Gi`.
2. **`deploy/k8s/externalsecret.yaml`:**
   - `ExternalSecret` (`external-secrets.io/v1beta1`) named `mealie-feeder-secret` in namespace `mealie`.
   - `secretStoreRef: { name: doppler-backend, kind: ClusterSecretStore }`, `refreshInterval: 1h`.
   - Maps `remoteRef.key: MEALIE_API_TOKEN` into Secret `mealie-feeder-secret` key `MEALIE_API_TOKEN`.
3. **`deploy/k8s/cronjob.yaml`:**
   - `CronJob` (`batch/v1`) named `mealie-feeder-sync` in namespace `mealie`.
   - `schedule: "0 */6 * * *"`, `concurrencyPolicy: Forbid`, `successfulJobsHistoryLimit: 3`, `failedJobsHistoryLimit: 3`, `startingDeadlineSeconds: 600`.
   - `jobTemplate.spec.activeDeadlineSeconds: 1800`, `backoffLimit: 1`.
   - Pod `securityContext`: `runAsNonRoot: true`, `runAsUser: 10001`, `runAsGroup: 10001`, `fsGroup: 10001`, `seccompProfile: { type: RuntimeDefault }`.
   - Container `securityContext`: `allowPrivilegeEscalation: false`, `readOnlyRootFilesystem: true`, `capabilities: { drop: ["ALL"] }`.
   - Image: `ghcr.io/paulojauregui/mealie-feeder:latest` (pinned tag in `k8s-gitops` overlay).
   - Args: `["--log-format=json", "sync", "--max-per-run=100"]`.
   - Env: `MEALIE_URL=http://mealie.mealie.svc.cluster.local:9000`, `MEALIE_FEEDER_DB=/var/lib/mealie-feeder/state.db`, `MEALIE_API_TOKEN` from `secretKeyRef: mealie-feeder-secret`.
   - Volume mounts: `mealie-feeder-state` at `/var/lib/mealie-feeder` + `emptyDir` at `/tmp`.
   - Resource requests/limits: `requests: { cpu: 50m, memory: 64Mi }`, `limits: { cpu: 500m, memory: 256Mi }`.

---

## 3. Starter Assets Contract Test (`internal/config/starter_assets_test.go`)

```go
package config_test
```

Deterministic assertions executed offline against `../../configs/sources.yaml` and `../../seeds/greatest-hits.yaml`:
1. `LoadSources` parses `configs/sources.yaml` with zero unknown fields and passes `Validate()`.
2. Verifies exact count of 14 enabled starter sources, 2 disabled starter sources (`smittenkitchen.com`, `thespruceeats.com`), and 4 excluded domains (`foodnetwork.com`, `food52.com`, `cooking.nytimes.com`, `americastestkitchen.com`).
3. `LoadSeeds` parses `seeds/greatest-hits.yaml` with zero unknown fields and passes `Validate()`.
4. Verifies every seed entry's `Domain` resolves to an enabled, non-excluded source in `configs/sources.yaml` (all 320 entries resolve to enabled sources; the 23 `thespruceeats.com` entries removed/replaced), every seed URL passes regex matching (`allow_patterns` / `!deny_patterns`) of its corresponding source, zero duplicate URLs exist, and all 10 culinary traditions have exact count of 32 entries (320 total).

---

## 4. Verification Command

```bash
CGO_ENABLED=0 go test -v -race -cover ./...
```
