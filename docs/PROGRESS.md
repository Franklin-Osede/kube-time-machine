# Estado del proyecto — kube-time-machine

> **Living document.** Snapshot del estado del repo entre sesiones. Se actualiza al cerrar cada sesión.
> **Última actualización:** 2026-05-20 (Etapa 6 cerrada — chart instalable end-to-end)

---

## TL;DR

`kube-time-machine` es "git blame para clusters de Kubernetes". **Etapas 1–6 cerradas — empaquetado completo, instalable con `helm install`.**

- ✅ Motor de deltas (100% cobertura + fuzz test)
- ✅ Storage local en filesystem (con index reconstruible)
- ✅ Agente: Buffer + Snapshotter + marshal + Informers + `cmd/agent/main.go` con flags, errgroup y SIGTERM-handling
- ✅ CLI `ktm`: `snapshot list/show`, `diff` con colored unified diff, `blame` con timeline correcta (incluye deletes en FULL ticks), `rollback` con optimistic locking nativo de K8s
- ✅ `internal/kubeclient` compartido entre agente y CLI
- ✅ Chart Helm con 6 templates (SA, ClusterRole+Binding read-only, PVC, Deployment, NetworkPolicy) + `_helpers.tpl`
- ✅ Dockerfile multi-stage → distroless static nonroot, 41 MB total
- ✅ CI GitHub Actions: fmt + vet + test + race + build + helm lint/template

Lo siguiente es **Etapa 7: polish** (Mermaid, ADRs 0003-0005 pendientes, demo recording, release.yml para publicar imagen, post draft).

---

## Mapa rápido de etapas

| Etapa | Outcome | Estado |
|---|---|---|
| 1 | Scaffolding del repo | ✅ Done |
| 3 | Motor de deltas | ✅ Done (fuzz + 100%) |
| 2.1 | `pkg/types` + `internal/storage` (FS) | ✅ Done |
| 2.2 | `internal/agent`: Buffer + Snapshotter + marshal | ✅ Done |
| 2.3 | `internal/agent/informers.go` + `cmd/agent/main.go` | ✅ Done |
| 4 | CLI cobra (snapshot list/show, diff) | ✅ Done |
| 5 | blame + rollback con optimistic locking | ✅ Done |
| 6 | RBAC + Helm + Dockerfile + CI | ✅ Done (smoke test contra OrbStack) |
| **7** | **Polish: Mermaid, ADRs 0003-0005, demo, release.yml, post draft** | 🚧 **Próximo** |
| 8 | Lanzamiento público | ⏳ |

Etapa 3 fue antes que Etapa 2 deliberadamente — ver [ADR-0002](adr/0002-incremental-deltas-with-reference-snapshots.md).

---

## Lo que funciona end-to-end hoy

Pipeline completo, ejecutable contra cualquier cluster (kind, minikube, prod):

```
[Kubernetes API] ─watch─► [SharedInformerFactory]
                                  │
                                  ▼ OnAdd/Update/Delete
                          [marshal.go]
                                  │
                                  ▼ Upsert / Delete
                              [Buffer]
                                  │
                                  ▼ Snapshot() (cada N intervalos)
                          [delta.Snapshot]
                                  │
                                  ▼ Compute(prev, curr)
                            [delta.Delta]
                                  │
                                  ▼ PutFull / PutDelta
                        [/var/lib/ktm (PVC)]
```

`cmd/agent/main.go` wirea todo: resuelve kubeconfig en orden kubectl-style (flag → in-cluster → `$KUBECONFIG` → `~/.kube/config`), arranca `Informers.Start` y `Snapshotter.Run` bajo `errgroup.WithContext`, atiende SIGINT/SIGTERM, y hace un flush final con 5s de budget antes de salir.

**Probarlo localmente:**

```bash
make build
./bin/ktm-agent --kubeconfig ~/.kube/config --storage-dir /tmp/ktm --interval 30s --full-every 4
# en otra terminal: kubectl edit deployment ... y ver aparecer ficheros en /tmp/ktm
```

**Falta para el MVP completo:** CLI (Etapa 4), blame + rollback (Etapa 5), empaquetado Helm/Docker (Etapa 6), polish y lanzamiento (7-8).

---

## Smoke test del chart (Etapa 6, 2026-05-20)

Validado contra **OrbStack K8s 1.33** con `helm install ktm deploy/helm -n ktm-test --set image.repository=ktm-agent --set image.tag=dev --set image.pullPolicy=Never --set snapshot.intervalSeconds=10 --set snapshot.fullEvery=3`.

| Comprobación | Resultado |
|---|---|
| `helm lint deploy/helm` | 0 errores (un INFO sobre icon, ignorado) |
| Pod arranca | `Running`, UID 65532, `readOnlyRootFilesystem: true` |
| PVC bound | `local-path` StorageClass, 1Gi |
| ClusterRole + ClusterRoleBinding aplicados | Nombre sufijado con namespace, sin colisión |
| NetworkPolicy aplicada | `ingress: []` + egress DNS + egress wide |
| Informers se sincronizan | `agent: informer caches synced` en logs |
| Snapshots persisten | Directorios `20260520THHMMSSmmmZ` cada 10s con cadencia FULL/delta/delta correcta |
| Permisos: nonroot escribe al PVC | OK gracias a `fsGroup: 65532` |
| `helm uninstall` no deja basura cluster-scoped | ClusterRole + ClusterRoleBinding desaparecen también |

### Hallazgo relevante

**Inspección del PVC con distroless es no-trivial.** El contenedor no tiene shell ni `ls`, así que `kubectl exec` está fuera. Para validar contenido del PVC en CI o debug se necesita `kubectl debug --target=agent` con una imagen busybox y mirar `/proc/1/root/var/lib/ktm/`. Es feature, no bug, pero conviene documentarlo en el README de operación cuando se escriba.

## Smoke test contra K8s real (2026-05-20)

Validado contra **OrbStack K8s 1.33** con el agente corriendo `--interval 10s --full-every 3`.

| Operación | Resultado | Snapshot evidencia |
|---|---|---|
| `kubectl set image deployment/api nginx=nginx:1.27` | `modified` con `image: nginx:1.27` | `20260519T185936775Z` (4s después del cambio) |
| `kubectl patch configmap app-config --type merge -p '{"data":{"env":"staging"}}'` | `modified` con `data: {'env': 'staging', 'region': 'eu'}` (merge correcto) | `20260519T192036772Z` (1s después) |
| `kubectl delete configmap app-config` (cae en tick FULL) | Ausencia del recurso en el FULL — sin `removed` explícito | `20260519T193456776Z` (FULL post-delete) |
| `kubectl delete configmap app-config` (cae en tick delta) | `removed` con `ConfigMap/ktm-demo/app-config` | `20260520T054806674Z` |
| `Ctrl-C` al agente | `final flush succeeded` + `stopped cleanly` en stderr | logs del agente |
| Cluster en steady state | Deltas vacíos (`{}`) | Cualquier delta entre cambios |

### Hallazgos relevantes

1. **Asimetría delete-en-FULL vs delete-en-delta.** El cadence policy (`full snapshot cada N`) hace que cuando un delete coincide con un tick FULL, la representación es por **ausencia en el siguiente FULL**, no por una entry `removed`. La información NO se pierde — pero implica que el futuro `ktm blame <kind>/<name>` (Etapa 5) no puede leer solo `removed`; tiene que **comparar el conjunto de keys entre snapshots consecutivos** para detectar deleciones implícitas en transiciones que pasan por un FULL. ⚠️ **Hay que tenerlo presente al implementar blame.**

2. **Sanitización valida en vivo.** Aunque incluimos `.status` y `resourceVersion` muta constantemente en el cluster, un cluster quieto produce deltas estrictamente vacíos (`{}`). Los riesgos teóricos sobre status-noise no se materializaron en el test. Mantenemos la decisión actual.

3. **Detección de zombies via ID milliseconds.** Durante el debug encontramos un agente zombie (PID de otra sesión) porque los IDs nuevos tenían milisegundos `.212` mientras los del agente esperado eran `.524`. **Cada start_time produce IDs con un offset de milisegundos único** — útil como huella forense del proceso que escribió cada snapshot.

4. **PascalCase en JSON keys.** El struct `delta.Key` no tenía JSON tags, así que las claves on-disk salían como `"Kind"`, `"Namespace"`, `"Name"`. Inconvenient para `jq` y consumidores externos. **Arreglado en el mismo día**: ahora son lowercase. Snapshots anteriores quedan ilegibles, pero como no hay usuarios todavía es buen momento.

---

## Historia de commits

| SHA | Mensaje | Etapa |
|---|---|---|
| `a33d3ea` | `feat(agent): wire main.go — the agent is no longer a stub` | 2.3 |
| `a649b7d` | `feat(agent): typed informers wired to the Buffer` | 2.3 |
| `cb931a9` | `docs(progress): add PROGRESS.md as living status document` | meta |
| `6f6baff` | `feat(agent): Kubernetes object → delta.State boundary` | 2.2 |
| `015c8b1` | `feat(agent): periodic Snapshotter with full + delta cadence` | 2.2 |
| `1183b6c` | `feat(agent): thread-safe Buffer for in-memory cluster state` | 2.2 |
| `5e28caa` | `feat(storage): local filesystem Store with rebuildable index` | 2.1 |
| `038b593` | `feat(types): public snapshot metadata (SnapshotID, Kind, Meta)` | 2.1 |
| `e1d5e45` | `docs(roadmap): mark Etapas 1 and 3 done, Etapa 2 in progress` | meta |
| `3485776` | `docs(adr): ADR-0002 incremental deltas with reference snapshots` | meta |
| `3c0dcc9` | `test(delta): fuzz the round-trip invariant` | 3 |
| `6039ed8` | `feat(delta): in-memory snapshot diff and apply (Etapa 3)` | 3 |
| `d4523c2` | `chore: scaffold MVP repository (Etapa 1)` | 1 |

---

## Cobertura de tests

| Paquete | Cobertura | Notas |
|---|---|---|
| `internal/delta` | **100%** | + fuzz test (verificado: 1.6M execs/10s sin counterexamples) |
| `internal/storage` | 83.5% | Ramas no cubiertas = errores I/O |
| `internal/agent` | 80.2% | Sin cubrir = ramas de type-assertion imposibles desde un informer tipado real + `slog.Error` en `Run` |
| `internal/cli` | 71.1% | Sin cubrir = wiring de cobra + algunos error paths de I/O |
| `pkg/types` | — | Sin tests propios; ejercido vía storage |

**Race detector (`go test -race -count=3 ./...`)**: limpio. Descubrió y motivó un fix real en `Informers.Start` (distinguir context-cancel de sync-failure).

---

## Decisiones arquitectónicas tomadas

| # | Decisión | Donde vive |
|---|---|---|
| 0 | Packaging defaults: distroless, ClusterRole read-only, NP deny-ingress, Deployment+Recreate, solo `ci.yml` en Etapa 6 | [ADR-0007](adr/0007-packaging-defaults.md) |
| 1 | Deltas incrementales + reference snapshots cada N | [ADR-0002](adr/0002-incremental-deltas-with-reference-snapshots.md) |
| 2 | Storage = directorio por snapshot + `index.json` reconstruible | Código en [internal/storage/local.go](../internal/storage/local.go) + memoria del proyecto |
| 3 | Informers tipados (no dynamic) | Decidido conjuntamente; ADR pendiente |
| 4 | Buffer con `sync.RWMutex` encapsulado, `Snapshot()` devuelve copia del map | Código en [internal/agent/buffer.go](../internal/agent/buffer.go) |
| 5 | Sanitización en marshal: stripear `ResourceVersion`, `ManagedFields`, `Generation` | Código en [internal/agent/marshal.go](../internal/agent/marshal.go) |
| 6 | Format on-disk: JSON con `MarshalIndent`, entries ordenadas (determinismo) | Código en [internal/storage/local.go](../internal/storage/local.go) |
| 7 | Atomicidad: tempfile + `os.Rename`, sin `fsync` (defendido en comentario) | Idem |

**Pendientes de formalizar en ADRs:**
- ADR-0003: informers tipados vs dynamic
- ADR-0004: índice reconstruible
- ADR-0005: sanitización de campos K8s

(Se pueden escribir en Etapa 7 — Polish — como parte del trabajo de pulir la doc antes del lanzamiento.)

---

## Próxima sesión: Etapa 7 — polish

El MVP es funcional e instalable. Etapa 7 prepara la doc y los artefactos para el lanzamiento público.

**Trabajo a hacer:**

| Item | Razón |
|---|---|
| Mermaid diagram en `docs/architecture.md` | El bloque ASCII actual es ilegible en GitHub; un Mermaid renderiza nativo |
| ADR-0003 (informers tipados vs dynamic) | Pendiente desde Etapa 2 — la decisión está tomada en código, falta documentarla |
| ADR-0004 (índice reconstruible) | Pendiente desde Etapa 2.1 |
| ADR-0005 (sanitización de campos K8s) | Pendiente — alimenta a ADR-0006 |
| `.github/workflows/release.yml` | On tag `v*`: build multi-arch + push a ghcr.io + adjuntar binarios al release. Aplazado de Etapa 6 |
| `docs/install.md` corto | Instrucciones de `helm install` con todos los flags relevantes, mención al gap de Recreate, troubleshooting de `kubectl debug` con distroless |
| Grabación del demo (5 min) | Install → break a deployment → diff → rollback → app recovers |
| Draft del launch post | Borrador en `docs/launch.md` (no commitear final hasta lanzamiento) |

**Decisiones abiertas para esta etapa:**

1. **Multi-arch en `release.yml`.** ¿`amd64` solo o también `arm64`? Apple Silicon es mainstream, vale la pena pagar el tiempo de build. Recomendación: `linux/amd64,linux/arm64` con `docker buildx`.
2. **Tag scheme.** `v0.1.0` para el primer release público. Helm chart `version` va parejo. Pendiente: ¿separar Chart version y App version desde ya, o moverlas juntas hasta v1.0?
3. **GHCR vs Docker Hub.** GHCR es la respuesta por defecto (anclado al repo, sin rate limits, autenticación GitHub-native). Docker Hub requeriría secrets aparte.
4. **Riesgo abierto del NP de status-noise.** Antes del lanzamiento conviene decidir si añadir `--no-status` a `ktm diff` o stripear `.status` en `marshal.go`. Ver tabla de riesgos abajo.

---

## Cómo correr lo que hay hoy

```bash
make build                                                            # compila bin/ktm-agent (real) y bin/ktm (stub aún)
make test                                                             # toda la suite
go test -race -count=3 ./...                                          # race detector con repetición
go test -fuzz=FuzzRoundTrip -fuzztime=30s ./internal/delta/           # fuzz manual del invariante
go test -cover ./...                                                  # cobertura
./bin/ktm-agent --kubeconfig ~/.kube/config --storage-dir /tmp/ktm \
    --interval 30s --full-every 4                                     # arrancar contra cluster local
```

---

## Estructura actual del repo

```
kube-time-machine/
├── cmd/
│   ├── agent/main.go          ← ✅ wiring real (flags, errgroup, SIGTERM, flush final)
│   └── ktm/main.go            ← ✅ wiring cobra
├── internal/
│   ├── delta/                 ← ✅ motor de diffs (100% + fuzz)
│   │   ├── delta.go
│   │   ├── delta_test.go
│   │   └── fuzz_test.go
│   ├── storage/               ← ✅ persistencia FS (83.5%)
│   │   ├── interface.go
│   │   ├── local.go
│   │   └── local_test.go
│   ├── cli/                   ← ✅ CLI (71.1%)
│   │   ├── diff.go
│   │   ├── diff_test.go
│   │   ├── reconstruct.go
│   │   ├── reconstruct_test.go
│   │   ├── root.go
│   │   ├── snapshot.go
│   │   └── snapshot_test.go
│   └── agent/                 ← ✅ completo (80.2%)
│       ├── buffer.go
│       ├── buffer_test.go
│       ├── informers.go
│       ├── informers_test.go
│       ├── marshal.go
│       ├── marshal_test.go
│       ├── snapshot.go
│       └── snapshot_test.go
├── pkg/
│   └── types/snapshot.go      ← ✅ tipos públicos
├── deploy/helm/               ← ✅ chart completo
│   ├── Chart.yaml
│   ├── values.yaml
│   └── templates/
│       ├── _helpers.tpl
│       ├── deployment.yaml    ← Recreate, replicas:1, distroless nonroot
│       ├── networkpolicy.yaml ← deny-ingress, allow DNS + wide egress
│       ├── pvc.yaml
│       ├── role.yaml          ← ClusterRole read-only
│       ├── rolebinding.yaml
│       └── serviceaccount.yaml
├── docs/
│   ├── adr/                   ← ADR-0001, 0002, 0006, 0007 (0003-0005 pendientes en Etapa 7)
│   ├── architecture.md
│   ├── comparison.md
│   ├── roadmap.md
│   └── PROGRESS.md            ← este fichero
├── .github/workflows/ci.yml   ← ✅ fmt + vet + test + race + build + helm lint/template
├── Dockerfile                 ← ✅ multi-stage → distroless static nonroot (41 MB)
├── .dockerignore
├── Makefile
├── README.md
├── LICENSE
├── go.mod                     ← Go 1.26 + k8s.io/api + k8s.io/apimachinery
└── go.sum
```

---

## Riesgos abiertos

| Riesgo | Estado | Plan |
|---|---|---|
| Curva de aprendizaje de client-go | **Activo** | Planificar 3 decisiones de diseño antes de tirar código en próxima sesión |
| Rollback puede romper clusters | Pendiente (Etapa 5) | Probar primero en kind/minikube, nunca cluster real hasta pulir |
| Storage local se llena sin retención automática | Pendiente (Phase 2 P7) | Warning logs cuando >80% — todavía no implementado |
| Status noise en diff durante rollouts | **Activo** | Smoke test CLI 2026-05-20: el diff de un `kubectl set image` muestra 4 hunks: 1 meaningful (el image change) y 3 derivados del rollout (`observedGeneration`, `lastUpdateTime`, ReplicaSet hash). En cluster quieto siguen siendo deltas vacíos, así que el riesgo solo se materializa post-cambio. Soluciones candidatas: (a) flag `--no-status` en `ktm diff`, (b) stripear `.status` en `marshal.go` (decisión global, ADR-future). Vivible para MVP, revisar antes de Etapa 7 con datos de demo. |
| Smoke test real contra un cluster | ✅ **Hecho 2026-05-20** | Add/Update (Deployment image + ConfigMap patch) y Delete (en delta y en full) validados end-to-end contra OrbStack K8s 1.33 |
| Smoke test del chart Helm | ✅ **Hecho 2026-05-20 (Etapa 6)** | Install/uninstall completo en OrbStack K8s 1.33: pod nonroot, RBAC cluster-scoped, PVC, NetworkPolicy, snapshots persisten al PVC con cadencia esperada |

---

## Links útiles

- [README.md](../README.md) — público, lo que ve un visitante
- [docs/roadmap.md](roadmap.md) — etapas alto nivel
- [docs/architecture.md](architecture.md) — diagrama + modelo de datos
- [docs/comparison.md](comparison.md) — vs Velero, ArgoCD, kubectl events
- [docs/adr/](adr/) — decisiones arquitectónicas
- Repo: <https://github.com/Franklin-Osede/kube-time-machine>
