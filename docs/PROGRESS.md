# Estado del proyecto — kube-time-machine

> **Living document.** Snapshot del estado del repo entre sesiones. Se actualiza al cerrar cada sesión.
> **Última actualización:** 2026-05-19

---

## TL;DR

`kube-time-machine` es "git blame para clusters de Kubernetes". Hoy tenemos construida **la mitad del MVP**:

- ✅ El motor de deltas (100% cobertura + fuzz test)
- ✅ El storage local en filesystem (con index reconstruible)
- ✅ Las tres piezas Go-puras del agente: Buffer, Snapshotter, marshal

Lo que falta para terminar el agente son **los informers de client-go** y el **wiring final** en `cmd/agent/main.go`. Después: CLI, blame, rollback, Helm, lanzamiento.

---

## Mapa rápido de etapas

| Etapa | Outcome | Estado |
|---|---|---|
| 1 | Scaffolding del repo | ✅ Done |
| 3 | Motor de deltas | ✅ Done (fuzz + 100%) |
| 2.1 | `pkg/types` + `internal/storage` (FS) | ✅ Done |
| 2.2 | `internal/agent`: Buffer + Snapshotter + marshal | ✅ Done |
| **2.3** | **`internal/agent/informers.go` + `cmd/agent/main.go`** | 🚧 **Próximo** |
| 4 | CLI cobra (snapshot list/show, diff) | ⏳ |
| 5 | blame + rollback | ⏳ |
| 6 | RBAC + Helm + Dockerfile + CI | ⏳ |
| 7 | Polish: Mermaid, ADRs, demo, post draft | ⏳ |
| 8 | Lanzamiento público | ⏳ |

Etapa 3 fue antes que Etapa 2 deliberadamente — ver [ADR-0002](adr/0002-incremental-deltas-with-reference-snapshots.md).

---

## Lo que funciona end-to-end hoy

Pipeline Go-puro, sin K8s ni red, testable con `t.TempDir()`:

```
[*appsv1.Deployment] ─marshal─► [delta.State] ─Upsert─► [Buffer]
                                                            │
                                                            ▼ Snapshot()
                                                       [delta.Snapshot]
                                                            │
                                                            ▼ Compute(prev, curr)
                                                       [delta.Delta]
                                                            │
                                                            ▼ PutFull / PutDelta
                                                       [filesystem]
```

**Falta para que esto se mueva solo:**
1. Un informer que reciba eventos del API server y llame a `marshal` + `buffer.Upsert`/`Delete`.
2. Un `main.go` que construya todas las piezas y arranque el `Snapshotter.Run` en una goroutine.

Sin esos dos, las piezas existen pero nadie las activa contra un cluster real.

---

## Historia de commits

| SHA | Mensaje | Etapa |
|---|---|---|
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
| `internal/agent` | 94.2% | Sin cubrir = el `slog.Error` en `Run` (no hay forma limpia de testearlo sin capturar stderr) |
| `pkg/types` | — | Sin tests propios; ejercido vía storage |

**Race detector (`go test -race ./...`)**: limpio en toda la suite.

---

## Decisiones arquitectónicas tomadas

| # | Decisión | Donde vive |
|---|---|---|
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

## Próxima sesión: cerrar Bloque B (Etapa 2.3)

**Dos ficheros, ambos introducen client-go.** Es la pieza con más curva de aprendizaje del proyecto.

### `internal/agent/informers.go`

Lo que vivirá ahí:
- `SharedInformerFactory` tipados para Deployments y ConfigMaps
- Handlers `OnAdd` / `OnUpdate` / `OnDelete` que llaman a `marshal.go` y luego a `buffer.Upsert` / `buffer.Delete`
- `WaitForCacheSync` para esperar al estado inicial antes de empezar a hacer snapshots
- Encapsulado en una struct `Informers` con métodos `Start(ctx)` y `WaitForSync(ctx)`

Vocabulario K8s nuevo: `clientcmd`, `kubernetes.Clientset`, `informers.SharedInformerFactory`, `cache.ResourceEventHandlerFuncs`, `WaitForCacheSync`, `ResourceVersion`.

### `cmd/agent/main.go`

Lo que vivirá ahí:
- Parseo de flags: `--kubeconfig`, `--storage-dir`, `--interval`, `--full-every`
- Detección automática in-cluster vs out-of-cluster
- Crear `*storage.Local`, `*agent.Buffer`, `*agent.Snapshotter`, `*agent.Informers`
- Lanzar `Snapshotter.Run` y `Informers.Start` en goroutines con `golang.org/x/sync/errgroup`
- Manejar SIGTERM con `signal.Notify` para shutdown limpio (flush final + cancel context)

Vocabulario nuevo: `rest.InClusterConfig`, `clientcmd.BuildConfigFromFlags`, `signal.Notify`, `errgroup.WithContext`.

---

## Decisiones pendientes (planificar antes de tirar código)

Antes de empezar la próxima sesión, decidir:

### 1. In-cluster vs out-of-cluster config

- **Opción A — Auto-detección.** Probar `rest.InClusterConfig()`; si falla, caer a `clientcmd.BuildConfigFromFlags("", *kubeconfigFlag)`. Más cómodo para desarrollo.
- **Opción B — Flag explícito.** Una opción `--mode=in-cluster|out-of-cluster`. Menos magia, errores más explícitos.
- **Recomendación inicial**: A. Es lo que hace `kubectl` y `kubelet`. La auto-detección K8s-style es esperada.

### 2. Resync period del informer

- **Opción A — `0`** (solo eventos en vivo, sin resync). Más simple, suficiente para MVP.
- **Opción B — `30min`/`1h`** ("asegúrate que estamos al día" periódicamente). Más resiliente a bugs hipotéticos del cache.
- **Recomendación inicial**: A. K8s garantiza que el informer cache se mantiene coherente; el resync periódico solo aporta paranoia. Si en producción aparece drift, lo activamos.

### 3. Estrategia de errores en handlers

- **Opción A — Log + skip.** Si `marshal` falla en un evento, loguear y continuar. El siguiente evento del mismo recurso lo arreglará.
- **Opción B — Work queue con retry.** Patrón canónico de operators. Más código.
- **Recomendación inicial**: A. Work queue es overkill para forensics — no necesitamos garantías de reconciliación, solo "el cluster cambió, regístralo".

### 4. Flush final al recibir SIGTERM

- **Opción A — Hacer un `Flush` antes de salir.** Captura el último estado antes de morir. ~50 líneas más en `main.go`.
- **Opción B — Salir sin más.** El próximo arranque toma un snapshot fresco. Más simple pero pierdes la ventana de cambios entre el último flush y el SIGTERM.
- **Recomendación inicial**: A. Es una pieza barata y mejora la calidad del histórico.

---

## Cómo correr lo que hay hoy

```bash
make build                                                            # compila bin/ktm-agent y bin/ktm (stubs aún)
make test                                                             # toda la suite
go test -race ./...                                                   # race detector
go test -fuzz=FuzzRoundTrip -fuzztime=30s ./internal/delta/           # fuzz manual del invariante
go test -cover ./...                                                  # cobertura
```

---

## Estructura actual del repo

```
kube-time-machine/
├── cmd/
│   ├── agent/main.go          ← stub "not implemented"
│   └── ktm/main.go            ← stub "not implemented"
├── internal/
│   ├── delta/                 ← ✅ motor de diffs (100%)
│   │   ├── delta.go
│   │   ├── delta_test.go
│   │   └── fuzz_test.go
│   ├── storage/               ← ✅ persistencia FS (83.5%)
│   │   ├── interface.go
│   │   ├── local.go
│   │   └── local_test.go
│   └── agent/                 ← ✅ piezas Go-puras (94.2%) | 🚧 falta informers + wiring
│       ├── buffer.go
│       ├── buffer_test.go
│       ├── marshal.go
│       ├── marshal_test.go
│       ├── snapshot.go
│       └── snapshot_test.go
├── pkg/
│   └── types/snapshot.go      ← ✅ tipos públicos
├── deploy/helm/               ← shell (templates llegan en Etapa 6)
├── docs/
│   ├── adr/                   ← ADR-0001, ADR-0002 (más en Etapa 7)
│   ├── architecture.md
│   ├── comparison.md
│   ├── roadmap.md
│   └── PROGRESS.md            ← este fichero
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
| Sanitización demasiado agresiva (Status incluido podría meter ruido) | **Activo** | Si la demo muestra muchos deltas espurios de status changes, ADR-future para stripear `.status` también |

---

## Links útiles

- [README.md](../README.md) — público, lo que ve un visitante
- [docs/roadmap.md](roadmap.md) — etapas alto nivel
- [docs/architecture.md](architecture.md) — diagrama + modelo de datos
- [docs/comparison.md](comparison.md) — vs Velero, ArgoCD, kubectl events
- [docs/adr/](adr/) — decisiones arquitectónicas
- Repo: <https://github.com/Franklin-Osede/kube-time-machine>
