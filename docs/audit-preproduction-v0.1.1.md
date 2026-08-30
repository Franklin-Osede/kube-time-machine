# Auditoría pre-producción — kube-time-machine v0.1.1

> **Superada por [`PRE-DEPLOY-AUDIT.md`](PRE-DEPLOY-AUDIT.md) (2026-08-24).**
> El veredicto NO-GO de este documento se emitió sobre el árbol previo al merge de
> `fix/prelaunch-correctness`. Los bloqueadores de código que describe están
> corregidos y verificados. Se conserva como registro histórico de la auditoría.

Fecha de auditoría: 2026-06-24  
Revalidación de fixes: 2026-06-25  
Revisión local: `dc810a6624a385fe4cdd0ff957c64dae7bbac1be` más cambios staged/unstaged sin commit.

## Veredicto

**NO-GO** para desplegar el agente in-cluster (Modo B) en producción.

El diseño Phase 1 es coherente para un grabador declarativo single-writer de
Deployments y ConfigMaps, y la suite local pasa con race detector. Los
bloqueadores de código R-02, R-03, R-04, R-05 y R-06 detectados el 2026-06-24
fueron corregidos y revalidados el 2026-06-25. El NO-GO actual no se debe a
capacidades Phase 2 ni a esos defectos, sino a que todavía no existe una unidad de release
reproducible e instalable:

1. No existe un release instalable v0.1.1: no hay tag, GitHub Release, imagen
   `ktm-agent:0.1.1` ni chart OCI `0.1.1`.
2. El árbol auditado no es reproducible desde una revisión Git: hay cambios
   staged y unstaged en release, agente, storage, CLI, chart y documentación.
3. El workflow de release y el install desde OCI aún no se han ejecutado ni
   validado con el contenido exacto de este árbol.

## Alcance verificado

KTM es un grabador de estado declarativo para incident response, no un backup,
GitOps ni observabilidad. El contrato está descrito en `README.md:30-40` y el
marshaller elimina `.status` y metadatos server-owned en
`internal/agent/marshal.go:122-153` y `internal/agent/marshal.go:203-219`.

El scope-lock nominal de Phase 1 es Deployments + ConfigMaps y PVC local
(`docs/roadmap.md:5-22`). Los dynamic informers existen como extensión
experimental (`deploy/helm/values.yaml:51-55`), aunque esto contradice el texto
estricto de `docs/roadmap.md:22`.

El flujo end-to-end actual es:

`typed/dynamic informers -> Buffer -> Snapshotter -> storage.Local`

Evidencia:

- Registro y arranque de informers: `cmd/agent/main.go:90-123`.
- Typed Add/Update/Delete: `internal/agent/informers.go:118-200`.
- Dynamic Add/Update/Delete: `internal/agent/dynamic_informers.go:109-195`.
- Copia del estado en RAM: `internal/agent/buffer.go:77-91`.
- Full/delta persistidos: `internal/agent/snapshot.go:111-147`.
- Escritura local durable: `internal/storage/local.go:251-278` y
  `internal/storage/local.go:302-345`.

## ¿Puedo instalar sin build from source?

| Artefacto | Estado | Evidencia |
|---|---|---|
| Código fuente v0.1.1 identificable | No | `README.md:6-10` declara launch prep; `CHANGELOG.md:13-17` mantiene 0.1.1 como Unreleased; el árbol local contiene cambios sin commit. |
| Tag Git `v0.1.1` | No | Consulta `git ls-remote --tags` del 2026-06-24: salida vacía. El estado esperado también está documentado en `docs/PROGRESS.md:185-200`. |
| GitHub Release con binarios | No | API pública de releases del 2026-06-24: `[]`; `README.md:51-52` y `docs/install.md:13` obligan a compilar. |
| Imagen `ghcr.io/franklin-osede/ktm-agent:0.1.1` | No | GHCR devolvió `MANIFEST_UNKNOWN` el 2026-06-24. El chart la referencia por defecto en `deploy/helm/values.yaml:11-14` y `deploy/helm/templates/_helpers.tpl:58-64`. |
| Chart OCI `ghcr.io/franklin-osede/charts/kube-time-machine:0.1.1` | No | GHCR devolvió `MANIFEST_UNKNOWN` el 2026-06-24. |
| Workflow capaz de publicar | Sí, no verificado con v0.1.1 | Imagen multi-arch: `.github/workflows/release.yml:32-92`; cinco plataformas y dos binarios: `.github/workflows/release.yml:94-140`; chart OCI: `.github/workflows/release.yml:142-180`; checksums: `.github/workflows/release.yml:182-210`. |

**Conclusión:** hoy no se puede instalar v0.1.1 sin compilar y publicar artefactos
propios.

## Tabla de riesgos

| ID | Severidad | Riesgo | Evidencia (archivo:línea) | Mitigación | ¿Aceptable? |
|---|---|---|---|---|---|
| R-01 | Bloqueador | No hay artefactos v0.1.1 instalables ni revisión congelada. | `README.md:6-10`, `CHANGELOG.md:13-17`, `docs/launch.md:3-11` | Congelar commit, publicar RC, verificar imagen/chart/binarios/checksums y luego tag final. | No |
| R-02 | Cerrado | GVR inexistente/RBAC denegado podía dejar `readyz=503` indefinidamente. | `cmd/agent/main.go:95-98` aplica `WithSyncTimeout(2*time.Minute)` cuando hay GVRs; mecanismo en `internal/agent/dynamic_informers.go:96-150`. | Corregido y cubierto por `internal/agent/dynamic_informers_test.go:186-249`. La duración sigue hard-coded. | Sí |
| R-03 | Cerrado con condición | PVC lleno podía impedir el full que disparaba GC. | GC corre antes de cada intento en `internal/agent/snapshot.go:268-277`; regresión en `internal/agent/snapshot_test.go:304-361`. | Corregido cuando existe historia anterior al anchor que pueda eliminarse. Mantener alertas y capacidad suficiente: GC no puede liberar un anchor único ni historia dentro de retención. | Sí con monitorización |
| R-04 | Cerrado | Dos agentes podían escribir concurrentemente si alguien saltaba el invariante de réplica única. | `cmd/agent/main.go:78-89` adquiere un writer lock; `internal/storage/local.go:74-119` mantiene el FD; Unix usa `flock` en `internal/storage/filelock_unix.go:11-17`; Windows usa `LockFileEx` en `internal/storage/filelock_windows.go:11-26`. | Corregido y cubierto por `internal/storage/local_test.go:49-89`. La CLI lectora no toma lock exclusivo para poder consultar mientras el agente graba. | Sí |
| R-05 | Cerrado | Fallo al escribir `index.json` dejaba un snapshot válido huérfano y el índice en memoria divergente. | `internal/storage/local.go:145-183` elimina el directorio si append falla; `internal/storage/local.go:293-312` construye una copia y restaura el índice anterior. | Corregido y cubierto por `internal/storage/local_test.go:497-554`. El cleanup sigue siendo best-effort si el filesystem también rechaza `RemoveAll`. | Sí |
| R-06 | Cerrado con condición | Readiness solo reflejaba informer sync y podía seguir OK durante fallos persistentes de storage. | `internal/agent/snapshot.go:57-69` mantiene estado de flush; `internal/agent/snapshot.go:175-209` expone health; `internal/agent/snapshot.go:328-333` registra resultado; `cmd/agent/main.go:128-130` degrada readyz tras 3 fallos consecutivos. | Corregido y cubierto por `internal/agent/snapshot_test.go:327-366`. Antes del primer intento no hay evidencia de storage failure, así que readiness sigue dependiendo de informers. | Sí con monitorización |
| R-07 | Alta | Rollback puede apuntar al cluster/contexto equivocado; `--yes` elimina la última pausa humana. | Resolución implícita de kubeconfig en `internal/kubeclient/kubeclient.go:19-44`; `--yes` en `internal/cli/rollback.go:56-59`; apply directo en `internal/cli/rollback.go:164-170` y `internal/cli/rollback.go:215-221`. | Exigir `--context` o `--expected-cluster`, imprimir server/context/namespace y requerir confirmación del nombre; prohibir `--yes` en prod por policy de equipo. | No para rollback de producción sin wrapper/guardrail |
| R-08 | Alta | Create-on-404 puede recrear un recurso eliminado deliberadamente. | `internal/cli/rollback.go:116-119`, `internal/cli/rollback.go:185-188`, `internal/cli/rollback.go:225-276`. | Separar en flag explícito `--allow-create`; por defecto abortar en 404. | Solo con aprobación explícita |
| R-09 | Media | Delete entre preview y Update devuelve un error genérico de Update, no una explicación TOCTOU accionable. | `internal/cli/rollback.go:164-169` y `internal/cli/rollback.go:215-220`. | Tratar `IsNotFound` igual que Conflict, indicando que se debe reejecutar y decidir si crear. | Sí, deuda operativa |
| R-10 | Media | Cobertura de rollback incompleta: no hay test de Conflict de ConfigMap, 404 de ConfigMap ni E2E real de `runRollback`; el supuesto test de dispatch no llama a `runRollback`. | Tests existentes en `internal/cli/rollback_test.go:124-175`, `internal/cli/rollback_test.go:322-374`; falso dispatch test en `internal/cli/rollback_test.go:376-386`. | Añadir los casos simétricos y un test con kubeconfig/fake transport o interfaz inyectable. | No si rollback entra en el runbook de prod |
| R-11 | Media | `DrainChanges` se ejecuta antes de saber si el flush tuvo éxito; un fallo degrada burst detection hasta que lleguen suficientes eventos nuevos. | `internal/agent/snapshot.go:268-273`, contador en `internal/agent/buffer.go:59-75`. | Drenar solo tras Put exitoso o restaurar el contador en error. | Sí con alerta; corregir antes de escala |
| R-12 | Media | Todo el estado actual y una copia del mapa viven simultáneamente en RAM; los payloads siguen referenciados, pero mapa/caches/delta añaden presión sobre el límite 256Mi. | `internal/agent/buffer.go:17-35`, `internal/agent/buffer.go:77-91`; límite en `deploy/helm/values.yaml:56-63`. | Medir con datos representativos; subir requests/limits; evitar habilitar recursos dinámicos sin sizing. | No validado para >500/1000 recursos |
| R-13 | Media | Eventos con error de marshal se descartan y el buffer conserva estado anterior sin retry/reconcile explícito. | `internal/agent/informers.go:141-157`, `internal/agent/informers.go:183-200`, `internal/agent/dynamic_informers.go:157-174`. | Métrica/alerta de marshal errors y resync/relist controlado. | Sí para typed; experimental para dynamic |
| R-14 | Media | `Get` y `Delete` aceptan SnapshotID como path sin validar traversal. En CLI, un ID controlado puede escapar de `snapshots/`. | `internal/storage/local.go:174-201`, `internal/storage/local.go:204-239`; SnapshotID se trata como opaco en `pkg/types/snapshot.go:12-16`. | Validar formato/segmento único y comprobar que el path resuelto permanece bajo root. | No en entornos multiusuario/no confiables |
| R-15 | Media | Payloads e índice se escriben 0644 y directorios 0755; ConfigMaps se almacenan en claro. | `internal/storage/local.go:61-63`, `internal/storage/local.go:255-257`, `internal/storage/local.go:306-334`; confidencialidad reconocida en `SECURITY.md:28-37`. | StorageClass cifrada, RBAC de PVC/debug restringido, copias locales cifradas y borrado post-incidente; considerar 0600/0700. | Solo con controles externos |
| R-16 | Media | El chart no soporta digest pin: el helper siempre construye `repository:tag`. | `deploy/helm/templates/_helpers.tpl:58-64`, valores disponibles en `deploy/helm/values.yaml:11-15`. | Añadir `image.digest` mutuamente exclusivo con tag y render `repository@sha256:...`. | Tag semver aceptable para RC; digest requerido para prod estricto |
| R-17 | Media | El PVC no tiene política `helm.sh/resource-policy: keep`, VolumeSnapshot ni backup declarativo; uninstall elimina historia. | PVC en `deploy/helm/templates/pvc.yaml:1-18`; uninstall destructivo documentado en `docs/install.md:142-148`. | Añadir keep configurable y procedimiento probado de VolumeSnapshot/backup. | No sin backup externo |
| R-18 | Baja | `storage.create=false` no acepta `existingClaim`; sigue montando `<fullname>-data`. El comentario BYO es engañoso. | `deploy/helm/values.yaml:32-36`, claim fijo en `deploy/helm/templates/deployment.yaml:89-92`, PVC condicional en `deploy/helm/templates/pvc.yaml:1-18`. | Añadir `storage.existingClaim` y usarlo en Deployment. | Sí si se precrea exactamente el nombre esperado |
| R-19 | Baja | La sintaxis dice que version es opcional para GVR, pero no hay discovery: el GVR vacío llega directamente al dynamic client. | `internal/agent/dynamic_informers.go:291-324`; `ForResource` recibe el GVR en `internal/agent/dynamic_informers.go:112-130`. | Exigir siempre `/version` o implementar RESTMapper/discovery. | No usar forma abreviada |
| R-20 | Baja | NetworkPolicy abre el puerto 8080 a cualquier origen seleccionado por la CNI; ese mismo servidor expone `/metrics`. | `deploy/helm/templates/networkpolicy.yaml:29-36`; `/metrics` en `internal/health/server.go:18-23` y `internal/health/server.go:54-59`. | Limitar origen si el CNI permite kubelet/monitoring selectors o separar health/metrics. | Sí; no expone snapshots |

## Agente

### Correcto

- Defaults de exclusión: `cmd/agent/main.go:53` y
  `deploy/helm/values.yaml:44-50`.
- Gate conjunto typed + dynamic antes del snapshotter:
  `cmd/agent/main.go:97-123`.
- Flush final con contexto nuevo de 5 s y solo tras sync:
  `cmd/agent/main.go:127-146`.
- El estado de la cadena avanza solo después de Put exitoso:
  `internal/agent/snapshot.go:120-147`.
- GC conserva un full anchor:
  `internal/agent/snapshot.go:164-228`.

### Incorrecto o incompleto

- El comentario del flush final habla de `inf.Ready()`, pero el código usa
  `allReady`: `cmd/agent/main.go:127-136`. El comportamiento es correcto; la
  documentación inline está desactualizada.
- No hay timeout en typed informers tampoco
  (`internal/agent/informers.go:78-97`), aunque el caso más probable y
  configurable es dynamic.
- No hay flush inmediato tras sync; el primero ocurre al vencer el intervalo
  (`internal/agent/snapshot.go:246-288`), por defecto 300 s
  (`deploy/helm/values.yaml:20-25`).
- Un `startupProbe` no resuelve el problema observado: liveness solo comprueba
  que HTTP responde (`internal/health/server.go:97-102`) y no falla durante un
  sync lento. Su ausencia no causa CrashLoop con las probes actuales.

## Storage

### Correcto

- Temp + fsync + rename + fsync de directorio:
  `internal/storage/local.go:302-345`.
- Payload antes de meta y limpieza en fallo:
  `internal/storage/local.go:251-278`.
- Rebuild de index ausente o corrupto:
  `internal/storage/local.go:71-139`.
- Colisión de ID al mismo milisegundo se rechaza sin overwrite:
  `internal/storage/local.go:255-260` y test
  `internal/storage/local_test.go:118-147`.

### Limitaciones

- Todos los métodos ignoran `context.Context`:
  `internal/storage/local.go:145-176`, `internal/storage/local.go:204-248`.
- No hay reserva de espacio, low-watermark ni clasificación de ENOSPC.
- La colisión no corrompe, pero pierde el flush; el ID solo tiene precisión de
  milisegundos (`internal/storage/local.go:294-300`).

## CLI y rollback

El rollback implementa correctamente ADR-0006:

- Captura el RV del Get y no hace re-Get:
  `internal/cli/rollback.go:124-162` y `internal/cli/rollback.go:193-214`.
- 409 produce mensaje accionable:
  `internal/cli/rollback.go:164-168` y `internal/cli/rollback.go:215-219`.
- El preview usa el mismo marshaller sanitizado:
  `internal/cli/rollback.go:131-143` y `internal/cli/rollback.go:195-204`.
- El default de storage CLI es `$HOME/.ktm/data`:
  `internal/cli/root.go:46-54`; el agente usa `/var/lib/ktm`:
  `cmd/agent/main.go:48-52`.

No se recomienda habilitar rollback como procedimiento estándar de producción
hasta resolver R-07, R-08 y R-10. Para uso solo lectura forense, esos riesgos no
bloquean el agente.

## Helm

### Correcto

- `replicas: 1` y `Recreate`: `deploy/helm/templates/deployment.yaml:1-14`.
- Non-root, fsGroup, seccomp, read-only rootfs y drop ALL:
  `deploy/helm/templates/deployment.yaml:22-32` y
  `deploy/helm/templates/deployment.yaml:79-83`.
- PVC RWO 10Gi por defecto: `deploy/helm/templates/pvc.yaml:9-17` y
  `deploy/helm/values.yaml:32-36`.
- RBAC default mínimo: `deploy/helm/templates/role.yaml:14-25`.
- Probes coherentes con health server:
  `deploy/helm/templates/deployment.yaml:57-78`.

### Gaps evaluados

- **startupProbe:** no bloqueador con la liveness actual; sí sería útil si
  liveness empieza a comprobar progreso real.
- **PodDisruptionBudget:** no aporta HA a una sola réplica y puede bloquear
  drains. No es un bloqueador; la indisponibilidad es una limitación Phase 1.
- **ServiceMonitor:** falta, aunque `/metrics` existe. Se debe declarar scrape o
  documentar PodMonitor/annotations antes de confiar en las métricas.
- **VolumeSnapshot/backup:** bloqueador operativo para historia crítica.
- **NetworkPolicy:** egress `0.0.0.0/0` en
  `deploy/helm/templates/networkpolicy.yaml:37-53`; es portable, pero no limita
  exfiltración de un Pod comprometido.

## CI/CD

CI actual:

- gofmt: `.github/workflows/ci.yml:24-31`.
- race: `.github/workflows/ci.yml:33-34`.
- vet/build: `.github/workflows/ci.yml:36-40`.
- Helm lint/render/health toggle: `.github/workflows/ci.yml:42-73`.

No incluye docker build, escaneo de imagen, `govulncheck`, firma/provenance,
`kubeconform`, helm unittest ni smoke E2E. Para v0.1.1, son especialmente
prioritarios:

1. `docker build` del Dockerfile real.
2. `govulncheck ./...`.
3. Trivy/Grype antes de push.
4. Cosign keyless para imagen y chart + provenance/SBOM.
5. `kubeconform` sobre `helm template`.
6. Smoke install con un cluster efímero y verificación de primer snapshot.

El release genera checksums de binarios, pero no firma checksums, imagen o chart
(`.github/workflows/release.yml:182-210`).

## Discrepancias documentación/código

1. Corregido el 2026-06-25: el runbook usa un contenedor `kubectl debug` para
   ejecutar `df` contra `/proc/1/root/var/lib/ktm`, no contra distroless.
2. Corregido el 2026-06-25: Deployment y PVC usan el fullname
   `ktm-kube-time-machine`.
3. Corregido el 2026-06-25: el primer tick es full porque
   `flushNum==0 % fullEvery == 0`; GC se ejecuta antes del intento de escritura.
4. Corregido el 2026-06-25: dynamic sync usa timeout de dos minutos cuando se
   configuran GVRs (`cmd/agent/main.go:95-98`).
5. El cuerpo real de readyz es `not ready\n`
   (`internal/health/server.go:112-122`), no el JSON documentado en
   `docs/runbook.md:18-21`.
6. Los mensajes de rollback del runbook (`docs/runbook.md:105-132`) no coinciden
   con `internal/cli/rollback.go:164-170`,
   `internal/cli/rollback.go:225-249`.
7. `docs/runbook.md:170` afirma que el siguiente full “bridges the gap”; borrar
   manualmente una pieza intermedia puede dejar PrevID roto. La reconstrucción
   falla si falta un predecessor (`internal/cli/reconstruct.go:35-55`).
8. `docs/roadmap.md:22` y `docs/roadmap.md:43` dicen “No metrics”/Phase 2, pero
   `/metrics` está activo en `cmd/agent/main.go:117-118` y
   `internal/health/server.go:54-59`.
9. ADR-0007 todavía dice que `release.yml` está diferido
   (`docs/adr/0007-packaging-defaults.md:53-70`), aunque existe y está completo.
10. `docs/PROGRESS.md:175-177` dice “sin fsync”, contradicho por
    `internal/storage/local.go:302-345`.
11. `docs/PROGRESS.md:320` mantiene retención como pendiente Phase 2, aunque ya
    existe en `internal/agent/snapshot.go:86-95` y
    `deploy/helm/values.yaml:26-30`.
12. `docs/install.md:148` dice que el PVC se elimina “along with the namespace”,
    pero `helm uninstall` no elimina el namespace creado con `--create-namespace`;
    sí elimina el PVC gestionado por el release.

## Bloqueadores absolutos y fix concreto

1. **Publicar artefactos reproducibles.**
   - Commit limpio y firmado.
   - Tag `v0.1.1-rc.1`.
   - Verificar digest multi-arch amd64/arm64, chart OCI, diez binarios y
     `checksums.txt`.
   - Instalar el RC desde OCI en un cluster efímero.
   - Publicar `v0.1.1` solo después del smoke.

2. **Probar el runbook en el cluster destino.**
   - Verificar que el runtime permite `kubectl debug --target=agent` y acceso a
     `/proc/1/root`; mantener un helper Pod aprobado como fallback.
   - Eliminar la recomendación de borrar snapshots intermedios.
   - Probar extracción y recuperación en el mismo tipo de cluster destino.

R-02, R-03, R-04, R-05 y R-06 ya no son bloqueadores. Quedan como mejoras
posteriores hacer configurable el timeout, añadir métricas explícitas de último
flush/último error, endurecer el cleanup best-effort y, si aparecen writers
externos al agente, exigir el writer lock en las operaciones `Put*`.

## Matriz por escenario

| Escenario | ¿KTM es adecuado? | Limitación |
|---|---|---|
| ¿Qué cambió en Deployment X? | Sí, tras resolver bloqueadores | Solo estado declarativo de Deployments/ConfigMaps por default. |
| Rollback de Deployment | Sí, con precaución | Guardrail de contexto ausente; `--yes`; Create-on-404. |
| Historia de Secrets | No | RBAC no incluye Secrets (`deploy/helm/templates/role.yaml:14-21`). |
| Backup DR del cluster | No | PVC local y sin restore de cluster. |
| Consulta in-cluster sin extraer PVC | No | La CLI lee filesystem local (`internal/cli/root.go:35-54`). |
| HA/multi-réplica | No | Single writer, RWO, Recreate. |
| >1000 Deployments | No validado | Buffer, informer caches y copias sin benchmark/sizing. |

## Checklist pre-deploy

No ejecutar en producción hasta cerrar los bloqueadores. Después:

```bash
export KTM_NS=ktm-system
export KTM_RELEASE=ktm
export KTM_CHART=oci://ghcr.io/franklin-osede/charts/kube-time-machine
export KTM_VERSION=0.1.1

helm show chart "${KTM_CHART}" --version "${KTM_VERSION}"
helm pull "${KTM_CHART}" --version "${KTM_VERSION}"

helm upgrade --install "${KTM_RELEASE}" "${KTM_CHART}" \
  --version "${KTM_VERSION}" \
  --namespace "${KTM_NS}" \
  --create-namespace \
  --values deploy/helm/values-prod.yaml \
  --atomic \
  --timeout 10m
```

Verificación:

```bash
kubectl -n "${KTM_NS}" rollout status deployment/ktm-kube-time-machine --timeout=10m
kubectl -n "${KTM_NS}" get pod,pvc,networkpolicy
kubectl auth can-i list deployments.apps \
  --as=system:serviceaccount:${KTM_NS}:ktm-kube-time-machine --all-namespaces
kubectl auth can-i list configmaps \
  --as=system:serviceaccount:${KTM_NS}:ktm-kube-time-machine --all-namespaces
kubectl auth can-i get secrets \
  --as=system:serviceaccount:${KTM_NS}:ktm-kube-time-machine --all-namespaces

POD="$(kubectl -n "${KTM_NS}" get pod \
  -l app.kubernetes.io/instance="${KTM_RELEASE}",app.kubernetes.io/name=kube-time-machine \
  -o jsonpath='{.items[0].metadata.name}')"

kubectl -n "${KTM_NS}" get --raw \
  "/api/v1/namespaces/${KTM_NS}/pods/${POD}:8080/proxy/readyz"
kubectl -n "${KTM_NS}" get --raw \
  "/api/v1/namespaces/${KTM_NS}/pods/${POD}:8080/proxy/metrics"
kubectl -n "${KTM_NS}" logs "${POD}" -c agent --since=10m | \
  grep -E 'informer caches synced|final flush|flush failed|gc'
```

El Pod puede estar Ready antes del primer snapshot. Esperar como mínimo
`snapshot.intervalSeconds` y verificar que `ktm_flushes_total` sea mayor que cero.

### Extracción del PVC

No usar `kubectl exec` contra el contenedor distroless. En una ventana de
mantenimiento, crear una copia debug del Pod que conserve el volumeMount,
detener el writer y copiar el mount desde la copia:

```bash
POD="$(kubectl -n "${KTM_NS}" get pod \
  -l app.kubernetes.io/instance="${KTM_RELEASE}",app.kubernetes.io/name=kube-time-machine \
  -o jsonpath='{.items[0].metadata.name}')"

# Sustituir la imagen por un digest aprobado por la organización.
kubectl -n "${KTM_NS}" debug "${POD}" \
  --copy-to=ktm-pvc-exporter \
  --container=agent \
  --set-image=agent=busybox:1.36.1 \
  -- sleep 3600

kubectl -n "${KTM_NS}" scale deployment/ktm-kube-time-machine --replicas=0
kubectl -n "${KTM_NS}" wait --for=delete "pod/${POD}" --timeout=5m

kubectl -n "${KTM_NS}" wait --for=condition=Ready pod/ktm-pvc-exporter --timeout=5m
mkdir -p /tmp/ktm-export
kubectl -n "${KTM_NS}" cp ktm-pvc-exporter:/var/lib/ktm/. /tmp/ktm-export
kubectl -n "${KTM_NS}" delete pod ktm-pvc-exporter
kubectl -n "${KTM_NS}" scale deployment/ktm-kube-time-machine --replicas=1

ktm --storage-dir /tmp/ktm-export snapshot list
```

La copia hereda el ServiceAccount y el mount del Pod original. En un
procedimiento endurecido, generar/revisar el YAML antes de crearlo, desactivar
el automount del token y usar una imagen por digest. Probarlo con el CSI destino;
RWO puede impedir el mount simultáneo si la copia no queda en el mismo nodo. Si
ocurre, escalar primero a cero y crear un helper Pod aprobado que monte el claim.

### Backup del PVC

- Preferido: `VolumeSnapshot` CSI programado, con política de retención y restore
  ensayado.
- Alternativa: job de copia a almacenamiento cifrado, siempre coordinado con el
  single writer o usando snapshots consistentes del backend.
- No considerar el backup válido hasta restaurar una copia y ejecutar
  `ktm snapshot list`, `show`, `diff` y `blame`.

### Upgrade

```bash
helm upgrade "${KTM_RELEASE}" "${KTM_CHART}" \
  --version "${KTM_VERSION}" \
  --namespace "${KTM_NS}" \
  --values deploy/helm/values-prod.yaml \
  --atomic \
  --timeout 10m
```

Reservar ventana de mantenimiento: `Recreate` causa un gap de captura
(`deploy/helm/templates/deployment.yaml:1-14`). Validar un full posterior al
upgrade; KTM puede observar el estado final, pero no reconstruir el instante
preciso de cambios ocurridos durante el gap.

## Preguntas abiertas para el equipo

1. Proveedor: EKS, GKE, AKS u on-prem.
2. CNI y semántica real de NetworkPolicy, incluida comunicación kubelet->Pod.
3. CSI/StorageClass y confirmación de cifrado en reposo.
4. Soporte de VolumeSnapshot y política de restore.
5. Número aproximado de Deployments, ConfigMaps y tasa de cambios.
6. Namespaces con ConfigMaps sensibles que deben excluirse.
7. Retención requerida por compliance y RPO/RTO del historial.
8. ¿Rollback estará permitido en producción o KTM será solo lectura forense?
9. Contextos/nombres de clusters de producción que debe validar el guardrail.
10. Sistema de métricas disponible: Prometheus Operator, managed Prometheus u otro.
11. Policy engine disponible para impedir réplicas >1 y cambios manuales al
    Deployment.
12. Tamaño inicial del PVC y capacidad de expansión online del StorageClass.

## Validaciones ejecutadas

- `gofmt -l .`: limpio.
- `go vet ./...`: pasa.
- `go build -trimpath ./cmd/agent ./cmd/ktm`: compila; el entorno restringido
  solo impidió escribir una entrada auxiliar del module cache.
- `go test -race -count=1 -timeout 5m ./...`: pasa completo fuera de la
  restricción de sockets del sandbox.
- Revalidación 2026-06-25:
  `TestSnapshotter_GCRunsBeforeFailedFullFlush`,
  `TestDynamicInformers_SyncTimeoutFiresBeforeParentContextCancel` y
  `TestPutFull_OrphanCleanedUpOnIndexFailure` pasan 10 veces bajo race.
- Revalidación adicional 2026-06-25:
  `TestWriterLock_RejectsSecondWriterButAllowsReaders` y
  `TestSnapshotter_FlushHealthDegradesAndRecovers` pasan 20 veces bajo race.
- Cross-build de los diez binarios del release
  (`linux/amd64`, `linux/arm64`, `darwin/amd64`, `darwin/arm64`,
  `windows/amd64`, para `ktm` y `ktm-agent`): compila.
- `helm lint deploy/helm`: pasa.
- `helm template ktm deploy/helm --namespace ktm-system`: pasa.
- Render con `storage.create=false`: no crea PVC, pero mantiene
  `claimName: ktm-kube-time-machine-data`.
- GitHub/GHCR, 2026-06-24: tags vacíos, releases `[]`, manifiestos 0.1.1 de
  imagen y chart inexistentes.
