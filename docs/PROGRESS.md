# Estado del proyecto — kube-time-machine

> **Living document.** Snapshot del estado del repo entre sesiones. Se actualiza al cerrar cada sesión.
> **Última actualización:** 2026-06-06 (pase de endurecimiento pre-RC en rama `fix/prelaunch-correctness` + auditoría independiente — P0 de release abierto)

---

## TL;DR

`kube-time-machine` es "git blame para clusters de Kubernetes". **Etapas 1–7 cerradas; Etapa 8 (lanzamiento) en progreso.** Tras un pase de endurecimiento pre-RC (ver sección dedicada) el código está listo para un RC técnico: el P0 de release (`release.yml` no publicaba `ktm-agent`) está **resuelto**, igual que el residual del flush final. Lo que queda es mecánica de lanzamiento: tag RC, verificar artefactos, demo y post.

- ✅ Motor de deltas (100% cobertura + fuzz test)
- ✅ Storage local en filesystem (con index reconstruible)
- ✅ Agente: Buffer + Snapshotter + marshal + Informers + `cmd/agent/main.go` con flags, errgroup y SIGTERM-handling
- ✅ CLI `ktm`: `snapshot list/show`, `diff` con colored unified diff, `blame` con timeline correcta (incluye deletes en FULL ticks), `rollback` con optimistic locking nativo de K8s
- ✅ `internal/kubeclient` compartido entre agente y CLI
- ✅ Chart Helm con 6 templates (SA, ClusterRole+Binding read-only, PVC, Deployment, NetworkPolicy) + `_helpers.tpl`
- ✅ Dockerfile multi-stage → distroless static nonroot, 41 MB total
- ✅ CI GitHub Actions: fmt + vet + test + race + build + helm lint/template
- ✅ Declarative-state recorder: `.status` stripped en marshal.go (ADR-0005) — KTM ya no compite con Velero/observabilidad
- ✅ Doc completa: architecture.md con Mermaid, comparison.md vs Velero/ArgoCD-Flux/events/observabilidad, install.md operacional
- ✅ ADRs 0003 (typed informers), 0004 (rebuildable index), 0005 (declarative-state), 0007 (packaging) documentados
- ✅ release.yml: multi-arch (amd64+arm64) image + chart OCI + binarios `ktm` **y** `ktm-agent` (5 plataformas) en GHCR on tag `v*`; `:latest` solo en tags estables

Lo siguiente es **Etapa 8: lanzamiento público** — cerrar el P0 de release, RC `v0.1.1-rc.1`, demo recording, launch post.

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
| 7 | Polish: Mermaid, ADRs 0003-0005, declarative-state framing, release.yml | ✅ Done |
| **8** | **Lanzamiento público — repo público + demo + post** | 🚧 **Próximo** |

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

**Falta para el MVP completo:** lanzamiento público (Etapa 8) — repo público, demo grabado, post publicado.

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
| `internal/agent` | 79.5% | Sin cubrir = ramas de type-assertion imposibles desde un informer tipado real + `slog.Error` en `Run` + rama defensiva de error de `stripStatus` (entrada inválida; `json.Marshal` no produce JSON corrupto en uso normal) |
| `internal/cli` | 65.1% | Sube desde 64.5% al añadir el test del conflict 409 de Update en Etapa 7 (ADR-0006 unhappy path) |
| `internal/kubeclient` | 55.6% | Tabla de precedencia (explicit > $KUBECONFIG > $HOME/.kube/config + error). Ramas no cubiertas: `rest.InClusterConfig()` (requiere Pod real) y el constructor del Clientset (boundary code sin lógica de business) |
| `pkg/types` | — | Sin tests propios; ejercido vía storage |

**Race detector (`go test -race -count=3 ./...`)**: limpio. Descubrió y motivó un fix real en `Informers.Start` (distinguir context-cancel de sync-failure).

---

## Decisiones arquitectónicas tomadas

| # | Decisión | Donde vive |
|---|---|---|
| 0 | Packaging defaults: distroless, ClusterRole read-only, NP deny-ingress, Deployment+Recreate, solo `ci.yml` en Etapa 6 | [ADR-0007](adr/0007-packaging-defaults.md) |
| 1 | Deltas incrementales + reference snapshots cada N | [ADR-0002](adr/0002-incremental-deltas-with-reference-snapshots.md) |
| 2 | Storage = directorio por snapshot + `index.json` cache reconstruible | [ADR-0004](adr/0004-rebuildable-index.md) |
| 3 | Informers tipados (no dynamic) | [ADR-0003](adr/0003-typed-informers.md) |
| 4 | Buffer con `sync.RWMutex` encapsulado, `Snapshot()` devuelve copia del map | Código en [internal/agent/buffer.go](../internal/agent/buffer.go) |
| 5 | KTM records declarative state, not observed state: stripear `ResourceVersion`, `ManagedFields`, `Generation`, `.status` | [ADR-0005](adr/0005-declarative-state-recorder.md) |
| 6 | Format on-disk: JSON con `MarshalIndent`, entries ordenadas (determinismo) | Código en [internal/storage/local.go](../internal/storage/local.go) |
| 7 | Atomicidad: tempfile + `os.Rename`, sin `fsync` (defendido en comentario) | Idem |
| 8 | Rollback usa live ResourceVersion del API server al apply time, no la del snapshot | [ADR-0006](adr/0006-rollback-live-resourceversion.md) |

---

## Próxima sesión: Etapa 8 — lanzamiento público

El código está. La doc está. El release pipeline está. Etapa 8 es la mecánica del lanzamiento.

> **Estado real de los artefactos (2026-06):** `release.yml` ya corrió en su día para `v0.0.1-test` y `v0.1.0`, así que **existen paquetes en GHCR** (imagen `ktm-agent` y chart OCI). Sin embargo **hoy no hay tags ni GitHub Releases visibles** (`git ls-remote --tags` y la API de releases devuelven vacío) — el tag/release fue retirado después de publicarse, dejando paquetes huérfanos. Decisión: **relanzar limpio con `v0.1.1`** (rc primero) en lugar de depender de artefactos sin tag asociado. Mientras tanto, README e install.md dirigen a *build from source*, que es lo único reproducible para un visitante hoy.

**Trabajo a hacer:**

| Item | Razón |
|---|---|
| Verificar el repo público | El repo ya es público en `github.com/Franklin-Osede/kube-time-machine`; revisar que README e instalación sean honestos antes de dirigir tráfico. |
| Push del tag de prueba `v0.1.1-rc.1` | Dispara `release.yml`: empuja imagen multi-arch a GHCR, sube binarios CLI, pushea chart OCI. Verificar los artefactos antes del tag final. |
| Push del tag final `v0.1.1` | Solo después de validar el release candidate y el install real desde los artefactos publicados. |
| Grabación del demo (5 min) | Install → modificar un Deployment → `ktm diff` → `ktm blame` → `ktm rollback` → app recovers |
| Launch post draft | Mantener `docs/launch.md` como borrador hasta tener el demo. Hooks: el problema de las 3 AM + framing declarative-state recorder + comparación con Velero/ArgoCD/observabilidad. |
| Habilitar GitHub Discussions | Canal de feedback antes que Issues — la barra de entrada para "tengo una idea vaga" es más baja. |

**Decisiones abiertas para esta etapa:**

1. **Versión inicial pública.** `v0.1.1` es la versión del lanzamiento. Chart version + appVersion están acoplados (ADR-0007); ambos se pinean al tag desde `release.yml`. Confirmar con `v0.1.1-rc.1` que GHCR acepta el push del chart OCI sin permisos extra más allá de `packages: write`.
2. **Plataformas del launch post.** LinkedIn (audiencia de Platform Engineering), HackerNews "Show HN" (técnica), Reddit r/kubernetes (técnica). Priorizar el orden — un post fallido en HN quema el carril.
3. **Phase 2 gate.** PROGRESS.md y roadmap mencionan el gate (50+ stars / 500+ likes / feature requests reales). Ya está documentado — no hace falta tocarlo hasta tener señal real.

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
│   └── agent/                 ← ✅ completo (79.5%)
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
│   ├── adr/                   ← ADR-0001 a 0007 (0003 typed-informers, 0004 rebuildable-index, 0005 declarative-state, 0006 rollback-rv, 0007 packaging)
│   ├── architecture.md        ← ✅ Mermaid + pipeline + on-disk layout + cross-refs
│   ├── comparison.md          ← ✅ vs Velero/ArgoCD-Flux/events/observabilidad
│   ├── install.md             ← ✅ helm install + distroless debug + Recreate gap
│   ├── roadmap.md
│   └── PROGRESS.md            ← este fichero
├── .github/workflows/
│   ├── ci.yml                 ← ✅ fmt + vet + test + race + build + helm lint/template
│   └── release.yml            ← ✅ on tag v*: multi-arch image + chart OCI + CLI binaries
├── Dockerfile                 ← ✅ multi-stage → distroless static nonroot (41 MB)
├── .dockerignore
├── Makefile
├── README.md
├── LICENSE
├── go.mod                     ← Go 1.26 + k8s.io/api + k8s.io/apimachinery
└── go.sum
```

---

## Endurecimiento pre-RC (2026-06-06, rama `fix/prelaunch-correctness` → PR #1)

Una segunda revisión profunda (más una auditoría independiente que la confirmó) encontró bugs de correctness que `go test ./...` en verde no revelaba. Arreglados, con tests de regresión, en 4 commits (código y docs separados):

| ID | Hallazgo | Fix |
|---|---|---|
| **C1** | El snapshotter arrancaba su ticker en paralelo al sync de informers → el primer full (siempre full) podía capturar una vista parcial del cluster. | `Informers.Ready()` (cerrado tras `WaitForCacheSync`); `Snapshotter.Run(ctx, ready)` espera esa compuerta antes del primer flush. |
| **C2** | `flushNum`/`prevID` avanzaban antes de la escritura → un full fallido dejaba el siguiente flush emitiendo un delta con `PrevID=""` (cadena no reconstruible). | El estado interno avanza **solo tras un `Put` exitoso**; un fallo reintenta el mismo slot de cadencia. |
| **C3** | El preview de rollback diffeaba el objeto live crudo contra el payload sanitizado → fugaba `status`/`managedFields`/`resourceVersion` justo en el momento de consentimiento. | El live se sanitiza con el mismo marshaller del agente (ADR-0005) antes de diffear. |
| **I1** | `release.yml` movía `:latest` para cualquier tag `v*`, incluido un RC. | `:latest` solo en tags estables; tags con guion se marcan prerelease. |
| **I5** | Perder `index.json` dejaba `list`/`blame` ciegos (el rebuild de ADR-0004 no estaba implementado). | `NewLocal` reconstruye el índice escaneando `snapshots/` cuando falta el cache, y lo re-persiste. |
| **I2** | `resources.watch` en `values.yaml` era config fantasma (informers y ClusterRole hardcodean el set). | Eliminado, con comentario de dónde vive de verdad el set vigilado. |
| **P0** | `release.yml` publicaba solo `ktm`, no `ktm-agent` — Modo A necesita ambos. | El job compila y adjunta ambos binarios para las 5 plataformas; `ktm-agent` gana `--version`. Verificado cross-compile. |
| **Flush final** | Residual de C1: el flush de shutdown corría incondicional aunque el sync no hubiera ocurrido. | Condicionado a un receive no-bloqueante de `inf.Ready()`. |

Verificado: `go build`, `go vet`, `gofmt -l` limpio, `go test -race -count=2 ./...` verde, y `ktm-agent` cross-compila en las 5 plataformas del release. No tocado deliberadamente: I4 (colisión de IDs a ms, baja) y `sync.Once` en `Start()` (blindaje opcional).

---

## Riesgos abiertos

| Riesgo | Estado | Plan |
|---|---|---|
| Release no publica `ktm-agent` | ✅ **Resuelto (2026-06-06)** | El job de `release.yml` ahora compila y adjunta `ktm` **y** `ktm-agent` para las 5 plataformas. Verificado: `ktm-agent` cross-compila en linux/darwin/windows (amd64+arm64). |
| Flush final puede persistir un full parcial | ✅ **Resuelto (2026-06-06)** | El flush de shutdown en `cmd/agent/main.go` está ahora condicionado a un receive no-bloqueante de `inf.Ready()`; si el agente se cancela antes del sync, se omite el flush en vez de persistir una vista parcial. |
| `index.json` corrupto no tiene fallback | ✅ **Resuelto (2026-06-07)** | Un Unmarshal fallido ahora cae al rebuild desde `snapshots/` con un warning y re-persiste un índice limpio; `NewLocal` ya no falla por un cache corrupto. |
| **Snapshot incompleto puede entrar al índice reconstruido** | Abierto (bajo) | meta.json se escribe antes que el payload; un crash entre medias deja un dir que el rebuild acepta (valida solo meta). Escribir payload antes que meta, o validar payload en el rebuild. |
| Sin tags ni GitHub Releases públicos | **Abierto** | `git ls-remote --tags origin` vacío y Releases API `[]`. Relanzar limpio con `v0.1.1` (RC primero ~2026-06-09, final ~2026-06-16). |
| Curva de aprendizaje de client-go | **Activo** | Planificar 3 decisiones de diseño antes de tirar código en próxima sesión |
| Rollback puede romper clusters | Pendiente (Etapa 5) | Probar primero en kind/minikube, nunca cluster real hasta pulir |
| Storage local se llena sin retención automática | Pendiente (Phase 2 P7) | Warning logs cuando >80% — todavía no implementado |
| Status noise en diff durante rollouts | ✅ **Resuelto (Etapa 7, 2026-05-23)** | Cerrado vía decisión de producto: KTM es declarative-state recorder, `.status` se stripea en `marshal.go` (ADR-0005). El diff post-`kubectl set image` ahora muestra solo el hunk relevante (image change), sin ruido derivado de `observedGeneration`/`lastUpdateTime`/ReplicaSet hash. |
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
