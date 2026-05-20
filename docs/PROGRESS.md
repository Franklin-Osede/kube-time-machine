# Estado del proyecto — kube-time-machine

> **Living document.** Snapshot del estado del repo entre sesiones. Se actualiza al cerrar cada sesión.
> **Última actualización:** 2026-05-20 (Etapa 5 cerrada — MVP funcional)

---

## TL;DR

`kube-time-machine` es "git blame para clusters de Kubernetes". **Etapas 1–5 cerradas — MVP funcional end-to-end.**

- ✅ Motor de deltas (100% cobertura + fuzz test)
- ✅ Storage local en filesystem (con index reconstruible)
- ✅ Agente: Buffer + Snapshotter + marshal + Informers + `cmd/agent/main.go` con flags, errgroup y SIGTERM-handling
- ✅ CLI `ktm`: `snapshot list/show`, `diff` con colored unified diff, `blame` con timeline correcta (incluye deletes en FULL ticks), `rollback` con optimistic locking nativo de K8s
- ✅ `internal/kubeclient` compartido entre agente y CLI

Lo siguiente es **Etapa 6: empaquetado** (RBAC + Helm templates reales + Dockerfile + CI). Luego polish (7) y lanzamiento (8).

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
| **6** | **RBAC + Helm + Dockerfile + CI** | 🚧 **Próximo** |
| 7 | Polish: Mermaid, ADRs, demo, post draft | ⏳ |
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

## Próxima sesión: Etapa 5 — blame + rollback

El agente captura, la CLI navega. Falta el remate del MVP: explicar la historia de un recurso concreto y revertirlo a un punto del pasado.

**Ficheros a crear:**

| Fichero | Responsabilidad |
|---|---|
| `internal/cli/blame.go` | `ktm blame <kind>/<namespace>/<name>` — timeline de cambios de un recurso |
| `internal/cli/rollback.go` | `ktm rollback <kind>/<namespace>/<name> --to <id>` con confirmación y optimistic locking |

> **Nota crítica heredada del smoke test:** `ktm blame` NO puede limitarse a leer entries `removed` de los deltas. Por la asimetría descubierta en 2026-05-20, las deleciones que coinciden con un tick FULL solo se representan como **ausencia en el siguiente FULL**, no como entry explícita. El algoritmo correcto: reconstruir el snapshot completo en cada punto del histórico y comparar el conjunto de keys con el anterior. Más CPU, pero no se pierden eventos.

**Decisiones de diseño pendientes para Etapa 5:**

1. **Rollback necesita un cliente K8s.** Hasta ahora la CLI solo lee filesystem. Para rollback, necesita escribir al cluster vía client-go. La detección in-cluster vs out-of-cluster es la misma que el agente — extraer `buildKubeConfig` a una pieza compartida (¿`internal/kubeclient/`?).
2. **Optimistic locking via ResourceVersion.** Antes de hacer `Update`, leemos el recurso actual del cluster, comparamos su ResourceVersion con el que tenemos guardado del último snapshot, y solo aplicamos si coinciden. Si no, abortamos con mensaje claro pidiéndole al usuario que reintente.
3. **Pero `ResourceVersion` lo stripeamos en marshal.** Hay que reintroducirlo: o (a) no stripear ResourceVersion del payload guardado (lo retomamos solo para rollback) o (b) guardar la ResourceVersion en metadata aparte. Opción (b) es más limpia — el snapshot sigue siendo determinista y la ResourceVersion vive en un campo separado del JSON.
4. **Confirmación interactiva obligatoria.** Mostrar un preview tipo `ktm diff` entre el estado actual y el target, y pedir `[y/N]` antes de aplicar. Flag `--yes` para CI scripts.
5. **`ktm blame` output**: ¿table simple `TS / OP / DELTA-SUMMARY`, o algo más rico estilo `git log -p`? Empezar con table para MVP, formato `-p` puede venir luego.

**Ficheros a crear:**

| Fichero | Responsabilidad |
|---|---|
| `cmd/ktm/main.go` | Wiring de cobra (reemplazar el stub) |
| `internal/cli/root.go` | Comando raíz, flags globales (`--storage-dir`) |
| `internal/cli/snapshot.go` | `ktm snapshot list` y `ktm snapshot show <id>` |
| `internal/cli/diff.go` | `ktm diff --from <id> --to <id> [--namespace foo]` con colores |

**Decisiones de diseño para esta etapa:**

1. **Librería de colores.** `github.com/fatih/color` es el estándar de facto. Soporta detección de TTY y respeta `NO_COLOR`.
2. **Formato del diff.** Algo tipo `git diff` con prefijos `+` (verde), `-` (rojo), espacios en común. Por recurso, mostrar Kind/Namespace/Name como cabecera.
3. **`--namespace foo` como filtro de diff.** Carga ambos snapshots completos, reconstruye via chain de deltas, filtra por namespace, calcula diff de strings JSON línea-a-línea.
4. **Path del storage por defecto.** Para uso local: `~/.ktm/data` o `$XDG_DATA_HOME/ktm`. Distinto del agente (`/var/lib/ktm`) porque la CLI normalmente corre fuera del cluster — necesita rsync/mount/escaneo de PVCs para leer datos producidos in-cluster. (Para MVP basta con que apunte a un dir local; integración cross-cluster es Phase 2.)

**Vocabulario nuevo:** `cobra.Command`, `cobra.AddCommand`, `pflag`, `cmd.Execute`, `RunE`, `PreRunE`.

**Dependencias a añadir:**
- `github.com/spf13/cobra`
- `github.com/fatih/color`

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
| Status noise en diff durante rollouts | **Activo** | Smoke test CLI 2026-05-20: el diff de un `kubectl set image` muestra 4 hunks: 1 meaningful (el image change) y 3 derivados del rollout (`observedGeneration`, `lastUpdateTime`, ReplicaSet hash). En cluster quieto siguen siendo deltas vacíos, así que el riesgo solo se materializa post-cambio. Soluciones candidatas: (a) flag `--no-status` en `ktm diff`, (b) stripear `.status` en `marshal.go` (decisión global, ADR-future). Vivible para MVP, revisar antes de Etapa 7 con datos de demo. |
| Smoke test real contra un cluster | ✅ **Hecho 2026-05-20** | Add/Update (Deployment image + ConfigMap patch) y Delete (en delta y en full) validados end-to-end contra OrbStack K8s 1.33 |

---

## Links útiles

- [README.md](../README.md) — público, lo que ve un visitante
- [docs/roadmap.md](roadmap.md) — etapas alto nivel
- [docs/architecture.md](architecture.md) — diagrama + modelo de datos
- [docs/comparison.md](comparison.md) — vs Velero, ArgoCD, kubectl events
- [docs/adr/](adr/) — decisiones arquitectónicas
- Repo: <https://github.com/Franklin-Osede/kube-time-machine>
