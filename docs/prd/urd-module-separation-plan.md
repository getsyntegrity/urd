# Urd: orden de trabajo y separación de módulos

Fecha: 7 de octubre de 2026. Documento de planificación; no sustituye el ADR pendiente ni demuestra capacidades por sí mismo.

## Decisión principal

La separación tiene dos momentos diferentes:

1. **Fronteras internas de paquetes y dependencias:** se definen en la fase 0 y se implementan en la fase 1 dentro del repositorio. Se conserva el módulo raíz y los módulos anidados existentes.
2. **Módulos Go independientes, con nuevos `go.mod` y publicación propia:** corresponden a la fase 3, opcional, después del Gate C. La extracción necesita evidencia de estabilidad y consumidores independientes.

La extracción publicada llega después de validar el core y usar sus contratos. Preparar fronteras internas después del baseline no autoriza publicar módulos independientes ni omitir los gates.

## Fuentes y alcance

| Fuente | Referencia utilizada | Función |
| --- | --- | --- |
| Arquitectura adjunta | `arquitectura-urd(3).html` (documento externo aportado por el mantenedor), análisis original sobre `c279719` | Diseño inicial de cuatro capas, fases y condiciones de extracción. |
| PRD vigente | [urd-platform-prd.md](https://github.com/getsyntegrity/urd/blob/01da644a9515355ed8c21de90d88566407ac2fcc/docs/prd/urd-platform-prd.md), especialmente secciones 3 y 6 | Amplía a ocho fronteras lógicas y establece el orden de entrega. |
| Auditoría | [i-00-epic-345-audit.md](https://github.com/getsyntegrity/urd/blob/01da644a9515355ed8c21de90d88566407ac2fcc/docs/prd/i-00-epic-345-audit.md) | Evidencia histórica y brechas; sus estados no reemplazan los issues vivos. |
| Código consultado | `develop` en `01da644a9515355ed8c21de90d88566407ac2fcc` | Contraste de dependencias y avances reales. |
| Tracker | Issues consultados el 7 de octubre de 2026 | Dependencias y aceptación de cada trabajo. |

El PRD conserva su propio baseline y la auditoría separa resultados históricos de comprobaciones posteriores. Los resultados con Go 1.26 y Go 1.27 se deben conservar identificados, sin reescribir unos como si fueran otros. El punto de partida descrito abajo es una fotografía de esta fecha y SHA; el tracker conserva el estado vigente.

## Qué queremos separar

El HTML plantea persistence, projection, tenancy y Urd. El PRD añade integration, testkit, workflow y management. Son fronteras lógicas; esta tabla no prescribe ocho módulos Go publicados.

| Frontera | Responsabilidad | Dependencias permitidas según el PRD |
| --- | --- | --- |
| Persistence — #342 | Journal, snapshots, scopes opacos, slices, reader, adapters y conformance. | No importa tenancy, projection ni el paquete raíz Urd. Los tipos específicos del backend quedan en adapters. |
| Projection — #343 | Ejecución, checkpoints, marcas, fencing, parking y recuperación. | Contratos públicos de persistence y tipos aprobados; codecs y métricas inyectados. |
| Tenancy — #344 | Identidad/contexto opcionales, perfiles, routing y lifecycle. | Contratos públicos de persistence y projection. |
| Urd — #345 | Dominio, APIs de escritura/lectura y composición. | Contratos públicos de las otras fronteras y GoAkt. |
| Integration — #395 | Envelopes durables, outbox, relay y publishers existentes. | Contratos públicos y puertos de publicación; sin importar Urd ni duplicar el runner. |
| Testkit — #396 | Fakes, drivers de fallos y harness reutilizable. | Contratos públicos y utilidades de test; producción no importa testkit. |
| Workflow — #397 | Consolidación de sagas/procesos, inbox, estado, intents, timers y compensación. | Contratos públicos y un puerto de despacho que Urd implementa e inyecta. |
| Management — #398 | Fachada autorizada de capacidades, auditoría y observabilidad. | Contratos públicos de control; delega las operaciones durables a su propietario. |

El inspector posterior mantiene un núcleo de observación genérico y enriquecimiento Urd opcional. No habilita mover el runtime a otro proyecto ni modificar GoAkt como parte de esta separación.

## Punto de partida real

- #427 está cerrado. #433 incorporó el test de claves persistidas y namespace a `develop`; demuestra el escenario de stores en memoria y scope Unscoped. No sustituye pruebas PostgreSQL ni otros modos de tenancy.
- #428 sigue abierto. La corrección del journal y su recuperación PostgreSQL ya tiene evidencia; quedan los criterios de snapshots y durable state. Resolver el journal no completa automáticamente el issue.
- #346 sigue abierto. Además de la recuperación, hay que contrastar todos sus criterios e inventarios antes de cerrarlo. La brecha de propagación de una partición no nula en B2 no equivale al criterio de estabilidad con 1, 3 y 5 nodos de #350.
- Ya se corrigieron offsets por tenant y se bloqueó la retención automática cuando falta la capacidad segura requerida. Eso no demuestra todavía la identidad completa de checkpoint, el registry durable de consumidores ni el cambio de versión de proyecciones.
- `persistence/scope.go` todavía importa tenancy. El runner depende de servicios de Urd. El protocolo protobuf mezcla datos persistidos y mensajes de engine. Son fronteras pendientes concretas.
- El módulo raíz conserva el `replace` temporal de GoAkt hacia `github.com/pablogore/goakt/v4 v4.5.7-actorof.1`; esto bloquea la publicación independiente bajo los criterios de #372.

## Orden de ejecución

| Paso | Trabajo | Condición de salida |
| --- | --- | --- |
| 1 | Completar [#428](https://github.com/getsyntegrity/urd/issues/428): snapshots y durable state sobre PostgreSQL, respetando los contratos existentes. | Evidencia real de adopción y recuperación para los criterios restantes. Si faltan adapters, delimitar ese trabajo explícitamente. |
| 2 | Terminar [#346](https://github.com/getsyntegrity/urd/issues/346), incluida la revisión del inventario y de cada criterio. | Baseline verificable, con brechas y resultados históricos diferenciados. |
| 3 | Aprobar [#347](https://github.com/getsyntegrity/urd/issues/347), el ADR de fronteras y contratos. | Decisiones de scope, reader, slice, checkpoint, command y adapters; ocho fronteras reconciliadas con el HTML. |
| 4 | Completar contratos, TCK y experimentos de fase 0: #348, #351, #352, #355 y las decisiones relacionadas. | [Gate A #387](https://github.com/getsyntegrity/urd/issues/387): mecanismo de reader elegido por evidencia antes de comprometer el nuevo esquema. `xid8` sigue siendo candidato. |
| 5 | Implementar el core y la separación interna de fase 1. | Reader y migraciones correctos, dependencias controladas y recursos acotados; sin nuevos `go.mod`. |
| 6 | Pasar [Gate B #388](https://github.com/getsyntegrity/urd/issues/388). | Conformance y fallos inyectados en memoria y PostgreSQL, con los modos de tenancy exigidos. |
| 7 | Entregar fase 2: read-side, lifecycle/perfiles de tenancy, integration y testkit de producto. Workflow, management e inspector son posteriores. | Uso real de contratos y evidencia específica de cada capacidad. |
| 8 | Pasar [Gate C #389](https://github.com/getsyntegrity/urd/issues/389). | SPI estable durante la ventana acordada y consumidores que justifiquen independencia. No exige terminar toda capacidad posterior. |
| 9 | Evaluar [#372](https://github.com/getsyntegrity/urd/issues/372), extracción opcional de fase 3. | Dependencias publicadas compatibles, sin fork/replace temporal, TCK y política de versiones. Tenancy necesita otro consumidor. |

Las tareas de diseño que no dependen del experimento pueden prepararse después del ADR. Esto no permite implementar el nuevo esquema del reader antes del Gate A.

## PRs de separación interna

Después del ADR, las piezas concretas son:

| Issue | Cambio esperado | Dependencia registrada |
| --- | --- | --- |
| [#349](https://github.com/getsyntegrity/urd/issues/349) | Hacer `Scope` opaco y quitar persistence → tenancy. | #347. Preservar representación persistida y semántica de Unscoped. |
| [#353](https://github.com/getsyntegrity/urd/issues/353) | Tests de arquitectura que hagan cumplir las dependencias aprobadas. | #347. Cada excepción temporal necesita un issue concreto. |
| [#363](https://github.com/getsyntegrity/urd/issues/363) | Separar tipos del journal y protocolo del engine. | #347. Preservar compatibilidad protobuf y claves persistidas. |
| [#364](https://github.com/getsyntegrity/urd/issues/364) | Inyectar decoder y métricas; desacoplar el runner de servicios Urd. | #353. Urd compone cifrado/adapters e inyecta sus implementaciones. |
| [#365](https://github.com/getsyntegrity/urd/issues/365) | Agrupar offsetstore, projection y runner bajo la frontera projection. | #362, #363 y #364. Mantener compatibilidad mediante aliases donde corresponda. |

Este es el tramo donde empezamos a ver la separación en código. No consiste en mover directorios y añadir módulos: primero se eliminan dependencias invertidas y se estabilizan contratos verificables. La ruta exacta de los tipos protobuf y la ubicación de adapters que sirven más de un contrato deben quedar resueltas en el ADR.

## Validación y límites

- Scope y tipos persistidos: conformance y compatibilidad de identificadores, bytes y serialización; mover un tipo no implica migrar datos.
- Fronteras: tests de imports y compilación de consumidores; preservar los módulos anidados existentes, incluido PostgreSQL.
- Reader: cero omisiones, progreso y elegibilidad bajo las condiciones acordadas. Un cursor por timestamp y un test sin commits tardíos no alcanzan.
- Projection: comprobar identidad de checkpoint, rollback, dedupe y fencing. La atomicidad de efecto, marca y offset requiere una transacción común; una publicación externa necesita outbox e idempotencia.
- Integración: PostgreSQL, cluster y carga se ejecutan en sus lanes explícitas. Los tests unitarios usan fakes/mocks; el CI de feature/hotfix mantiene la política de omitir integración real.
- Extracción: consumidores contra versiones publicadas y TCK versionado. No basta con que el workspace local compile mediante `replace`.

Los números de rendimiento del HTML son entradas provisionales de experimentos, no SLAs demostrados. El baseline histórico tampoco es garantía de los contratos futuros.

## Próximo resultado que buscamos

Cerrar correctamente #428 y #346 deja un punto de partida fiable. El siguiente entregable arquitectónico es el ADR #347. Luego se ejecutan el trabajo de fase 0 y Gate A, y la separación interna de fase 1 conforme a sus dependencias.

La publicación de persistence/projection como módulos independientes se decide recién tras Gate C. No necesita esperar a las 81 tareas completas, pero sí a las condiciones específicas de #372. Ese es el orden que mantienen juntos el PRD, la arquitectura y el tracker.
