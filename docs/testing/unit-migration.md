# Unit tests: migration to go-specs

First phase of epic #201 is finished: every unit test now uses go-specs, with mocks, stubs or fakes injected in place of real dependencies, and no file in any module imports testify or the removed generated `mocks/` package. This list has one row per unit test, with its status and the signal it was found with. Tests that need a real component or resource are listed separately as out of phase and are documented exceptions of the unit gate (see "Out of phase").

## How to read it

A test is a unit test when it needs no real component or resource: no database, broker, socket, subprocess, actor system or cluster. Its status is `migrated` when it uses go-specs. Every unit test is `migrated`; the `unit-test` gate (`go run ./.github/scripts/unitgate -strict`) keeps it that way by failing any `Test` function file that does not call `specs.Describe`. The migration unit is the test, so unit tests that share a file with actor tests are in the unit list.

The list is recorded at `develop` `0de4249`: 873 `Test` functions in the 8 modules, of which 617 are unit and 256 out of phase. Subtests are not listed; they travel with their parent test.

The dependencies column was computed before the migration, from static signals found in each test body and the local helpers it calls, plus a manual review of 65 ambiguous tests. It is kept as the record of what each test used to depend on. `none` means no signal was found. The labels mean:

- `fixed wait`: `pause.For` or `time.Sleep`. Replace with a controllable clock or a fake, or wait on an observable condition.
- `real timer`: a real ticker. Replace with a fake ticker.
- `temp file`: writes or reads a file. Replace with an in-memory writer or reader.
- `env-gated skip`: behavior depends on an environment variable. Inject the value.
- `local socket`: opens a loopback listener or HTTP test server. Replace with an in-memory transport.
- Stores from `testkit` are in-memory fakes already and are not listed.
- The `testify mock` and `generated mocks` labels of the first recording are gone: testify and the generated `mocks/` package no longer exist in the repository.

## Totals

| Module | Unit pending | Unit migrated | Out of phase | Total |
|---|---|---|---|---|
| `.` | 0 | 608 | 217 | 825 |
| `benchmark` | 0 | 0 | 0 | 0 |
| `example/cluster` | 0 | 0 | 30 | 30 |
| `publisher/kafka` | 0 | 2 | 1 | 3 |
| `publisher/nats` | 0 | 2 | 1 | 3 |
| `publisher/pulsar` | 0 | 2 | 1 | 3 |
| `publisher/websocket` | 0 | 2 | 6 | 8 |
| `test/compat` | 0 | 1 | 0 | 1 |
| **Total** | 0 | 617 | 256 | 873 |

## Unit tests

### Module `.`

#### `command`

| Test | Status | Dependencies to substitute |
|---|---|---|
| `TestCarrierDelegatesTenantSerializationToTenancyPackage` | migrated | none |
| `TestCarrierRoundTripExpectedRevisionAbsentStaysAbsent` | migrated | none |
| `TestCarrierRoundTripExpectedRevisionMaxUint64` | migrated | none |
| `TestCarrierRoundTripExpectedRevisionPresent` | migrated | none |
| `TestCarrierRoundTripOptionalFieldsAbsent` | migrated | none |
| `TestCarrierRoundTripOptionalFieldsPresent` | migrated | none |
| `TestCarrierRoundTripPreservesIdentity` | migrated | none |
| `TestEnvelopeDeriveDelegatesToMetadataDerive` | migrated | none |
| `TestEnvelopeDeriveRejectsNilPayload` | migrated | none |
| `TestEnvelopeExpectedRevisionAbsentSurvivesCarrierRoundTrip` | migrated | none |
| `TestEnvelopeWithoutExpectedRevisionUnaffectedByNewField` | migrated | none |
| `TestErrorAs` | migrated | none |
| `TestErrorClassification` | migrated | none |
| `TestErrorMessage` | migrated | none |
| `TestErrorUnwrap` | migrated | none |
| `TestErrorUnwrapNilCause` | migrated | none |
| `TestFailureWithCode` | migrated | none |
| `TestGenerateOperationID` | migrated | none |
| `TestGenerateOperationIDUniqueness` | migrated | none |
| `TestIdentityDefinedTypesAreDistinct` | migrated | none |
| `TestIntegrationMetadataEnvelopeResultCarrier` | migrated | none |
| `TestIntegrationRejectedResultCarriesReconstructedMetadata` | migrated | none |
| `TestMarshalMetadataUsesCanonicalKeys` | migrated | none |
| `TestMetadataDeriveCustomNotInherited` | migrated | none |
| `TestMetadataDeriveDeadlineMayOnlyShorten` | migrated | none |
| `TestMetadataDeriveDoesNotInheritExpectedRevision` | migrated | none |
| `TestMetadataDeriveInheritsCorrelationAndChainsCausation` | migrated | none |
| `TestMetadataDerivePrincipalInheritedUnlessOverridden` | migrated | none |
| `TestMetadataDeriveRejectsSameOperationID` | migrated | none |
| `TestMetadataDeriveTenantInheritedWhenUnspecified` | migrated | none |
| `TestMetadataDeriveTenantMustNotChange` | migrated | none |
| `TestMetadataElapsedDeadlineIsRecognized` | migrated | none |
| `TestNewCanceledDefaultCause` | migrated | none |
| `TestNewEnvelope` | migrated | none |
| `TestNewEnvelopeRejectsNilPayload` | migrated | none |
| `TestNewFailed` | migrated | none |
| `TestNewFailureRequiresMessage` | migrated | none |
| `TestNewMetadataCustomDefensiveCopy` | migrated | none |
| `TestNewMetadataCustomValue` | migrated | none |
| `TestNewMetadataRoot` | migrated | none |
| `TestNewMetadataWithCorrelationID` | migrated | none |
| `TestNewMetadataWithCustomAcceptsValidKeyValue` | migrated | none |
| `TestNewMetadataWithCustomRejectsCanonicalKey` | migrated | none |
| `TestNewMetadataWithCustomRejectsInvalidValue` | migrated | none |
| `TestNewMetadataWithCustomRejectsReservedPrefix` | migrated | none |
| `TestNewMetadataWithDeadline` | migrated | none |
| `TestNewMetadataWithExpectedRevisionPositive` | migrated | none |
| `TestNewMetadataWithExpectedRevisionZeroIsGenesisNotAbsence` | migrated | none |
| `TestNewMetadataWithPrincipal` | migrated | none |
| `TestNewMetadataWithTenant` | migrated | none |
| `TestNewMetadataWithTimestampDefault` | migrated | none |
| `TestNewMetadataWithTimestampOverride` | migrated | none |
| `TestNewMetadataWithoutDeadline` | migrated | none |
| `TestNewMetadataWithoutExpectedRevision` | migrated | none |
| `TestNewMetadataWithoutTenant` | migrated | none |
| `TestNewOperationID` | migrated | none |
| `TestNewPrincipal` | migrated | none |
| `TestNewRejected` | migrated | none |
| `TestNewRejectedConcurrencyConflictCodeCheckableWithoutStringInspection` | migrated | none |
| `TestNewSuccessNoState` | migrated | none |
| `TestNewSuccessRequiresState` | migrated | none |
| `TestNewSuccessWithState` | migrated | none |
| `TestNewTimedOutDefaultCause` | migrated | none |
| `TestOutcomeKindsMutuallyExclusive` | migrated | none |
| `TestOutcomeStringPerKind` | migrated | none |
| `TestOutcomeZeroValueInvalid` | migrated | none |
| `TestPayloadAsTypedExtraction` | migrated | none |
| `TestPrincipalIsAbstract` | migrated | none |
| `TestResultErrAsCommandError` | migrated | none |
| `TestStateAsFalseWhenNoState` | migrated | none |
| `TestStateAsTypedExtraction` | migrated | none |
| `TestUnmarshalMetadataIgnoresUnknownEgoCmdKey` | migrated | none |
| `TestUnmarshalMetadataRejectsExpectedRevisionOverflow` | migrated | none |
| `TestUnmarshalMetadataRejectsInvalidCustomValue` | migrated | none |
| `TestUnmarshalMetadataRejectsMalformedExpectedRevision` | migrated | none |
| `TestUnmarshalMetadataRejectsMissingOperationID` | migrated | none |
| `TestUnmarshalMetadataRejectsNegativeExpectedRevision` | migrated | none |
| `TestUnmarshalMetadataRejectsReservedBareKey` | migrated | none |
| `TestUnmarshalMetadataRejectsUnrecognizedEgoNamespace` | migrated | none |
| `TestValidationSentinelsAreDistinct` | migrated | none |

#### `compose`

| Test | Status | Dependencies to substitute |
|---|---|---|
| `TestSpecValidate_MinimalSpecsPass` | migrated | none |
| `TestSpecValidate_ReportsEveryProblem` | migrated | none |
| `TestSpecValidate_V1_ZeroFamilies` | migrated | none |
| `TestSpecValidate_V2_EventsStoreRequired` | migrated | none |
| `TestSpecValidate_V2_NotRequiredForDurableStateOnly` | migrated | none |
| `TestSpecValidate_V3_NotRequiredWithoutDurableState` | migrated | none |
| `TestSpecValidate_V3_StateStoreRequired` | migrated | none |
| `TestSpecValidate_V4_Projections` | migrated | none |
| `TestSpecValidate_V5_NilEventAdapterElement` | migrated | none |
| `TestSpecValidate_V5_TypedNilPerInterfaceField` | migrated | none |
| `TestSpecValidate_V6_DuplicatePublisherIDsPerKind` | migrated | none |
| `TestSpecValidate_V6_NilPublisher` | migrated | none |
| `TestSpecValidate_V7_NegativeShutdownTimeout` | migrated | none |
| `TestSpecValidate_V8_ReportsInFieldOrder` | migrated | none |
| `TestSpecValidate_V8_SkipsValuesV5AndV6Rejected` | migrated | none |
| `TestSpecValidate_V8_TruthfulDeclarationsPass` | migrated | none |
| `TestSpecValidate_V8_UndeclaredAdaptersAreNotInspected` | migrated | none |
| `TestSpecValidate_V8a_SlotPortMustBeDeclared` | migrated | none |
| `TestSpecValidate_V8b_DeclarationMatchesMethods` | migrated | none |
| `TestSpecValidate_V8b_DeclarationOnlyCapabilityIsOneDirectional` | migrated | none |
| `TestSpecValidate_V8b_ImpliedCapabilitiesAreSkipped` | migrated | none |
| `TestSpecValidate_V8b_UnknownCapabilityIsAccepted` | migrated | none |
| `TestSpecValidate_V8c_RequiredCapabilities` | migrated | none |
| `TestSpecValidate_ValidSpecPasses` | migrated | none |
| `TestStartError` | migrated | none |

#### `compose/goakt`

| Test | Status | Dependencies to substitute |
|---|---|---|
| `TestNew_G1_ClusterRequiresEntityKinds` | migrated | none |
| `TestNew_MissingRequiredDependencyFailsWithNothingStarted` | migrated | none |
| `TestNew_NegativeShutdownTimeoutFailsAtNew` | migrated | none |
| `TestNew_ReportsEveryProblem` | migrated | none |
| `TestNew_StartsNothing` | migrated | none |
| `TestNew_V8RejectsALyingPublisherWithNothingStarted` | migrated | none |
| `TestRuntime_NilBeforeStart` | migrated | none |
| `TestStart_CancelledContextStartsNothing` | migrated | none |
| `TestStart_ProbeFailureNamesTheStore` | migrated | none |
| `TestStop_NeverStartedClosesPublishers` | migrated | none |

#### `compose/internal/adapters`

| Test | Status | Dependencies to substitute |
|---|---|---|
| `TestStartAndProbe_Empty` | migrated | none |
| `TestStartAndProbe_SkipsTypedNil` | migrated | none |
| `TestStartAndProbe_StartsThenPingsEachInOrder` | migrated | none |
| `TestStartAndProbe_StopsAtFirstFailure` | migrated | none |

#### `compose/internal/lifecycle`

| Test | Status | Dependencies to substitute |
|---|---|---|
| `TestCleanup_RunsUnderWithoutCancelAndTimeout` | migrated | none |
| `TestNew_RejectsIncompleteSteps` | migrated | none |
| `TestNew_RejectsNegativeShutdownTimeout` | migrated | none |
| `TestStartAndStop_AreSerialized` | migrated | none |
| `TestStart_ChecksContextBeforeEachStep` | migrated | none |
| `TestStart_FailureAtEachStepRollsBackInReverseThenReleases` | migrated | none |
| `TestStart_IsSingleUse` | migrated | none |
| `TestStart_PanickingStepRollsBackReleasesAndFails` | migrated | none |
| `TestStart_RollbackAttemptsEveryUndoAndReportsEveryError` | migrated | none |
| `TestStart_RunsStepsInOrder` | migrated | none |
| `TestStart_StepsWithoutStopAreSkippedOnRollback` | migrated | none |
| `TestState_TransitionsAreVisibleInsideSteps` | migrated | none |
| `TestStop_AfterFailedStartIsNoOp` | migrated | none |
| `TestStop_FailureAtEachStepStillRunsTheRest` | migrated | none |
| `TestStop_IsIdempotent` | migrated | none |
| `TestStop_JoinsEveryError` | migrated | none |
| `TestStop_NeverStartedOnlyReleases` | migrated | none |
| `TestStop_UndoesEveryStepInReverseOrder` | migrated | none |

#### `egopb`

| Test | Status | Dependencies to substitute |
|---|---|---|
| `TestDescriptor_IsSoundAndCarriesTheModulePath` | migrated | none |

#### `encryption`

| Test | Status | Dependencies to substitute |
|---|---|---|
| `TestAESEncryptor_DecryptShortCiphertext` | migrated | none |
| `TestAESEncryptor_DecryptWithWrongKeyID` | migrated | none |
| `TestAESEncryptor_EncryptDecryptRoundTrip` | migrated | none |
| `TestAESEncryptor_EncryptProducesDifferentCiphertext` | migrated | none |

#### `engine`

| Test | Status | Dependencies to substitute |
|---|---|---|
| `TestBehaviorFrom` | migrated | none |
| `TestBehaviorKindAssignability` | migrated | none |
| `TestBehaviorPlacementError` | migrated | none |
| `TestBuildSpawnOptionsFromConfig` | migrated | none |
| `TestClassifyTenantBinding` | migrated | none |
| `TestEngineClusterKindsExposesUrdActors` | migrated | none |
| `TestDefaultLoggerIsKitLoggerGlobal` | migrated | none |
| `TestDiscardLoggerDisablesEveryLevel` | migrated | none |
| `TestUrdSpawnOptionsResolveThroughRuntime` | migrated | none |
| `TestEngineEraseEntityStoreErrors` | migrated | none |
| `TestEngineHotPathGuards` | migrated | none |
| `TestEngineProjectionLagComputation` | migrated | none |
| `TestEngineProjectionLagStoreErrors` | migrated | none |
| `TestEngineStartWithoutActorSystem` | migrated | none |
| `TestEntityFamily_String` | migrated | none |
| `TestErrBehaviorNotPointerMessage` | migrated | none |
| `TestMissingRequiredExtensionsSentinel` | migrated | none |
| `TestNewSpawnConfigRoundTrip` | migrated | none |
| `TestNewSpawnConfigSkipsNilOption` | migrated | none |
| `TestOptionWithEncryptor` | migrated | none |
| `TestOptionWithEventAdapters` | migrated | none |
| `TestOptionWithEventAdaptersMultiple` | migrated | none |
| `TestOptionWithLogger` | migrated | none |
| `TestOptionWithLoggerNilFallback` | migrated | none |
| `TestOptionWithOffsetStore` | migrated | none |
| `TestOptionWithProjection` | migrated | none |
| `TestOptionWithProjectionMultiple` | migrated | none |
| `TestOptionWithProjectionNil` | migrated | none |
| `TestOptionWithSnapshotStore` | migrated | none |
| `TestOptionWithStateStore` | migrated | none |
| `TestOptionWithTelemetry` | migrated | none |
| `TestOptionWithTelemetryNil` | migrated | none |
| `TestOptionWithTenantResolver` | migrated | none |
| `TestOptionWithTenantResolverAmbiguousCount` | migrated | none |
| `TestOptionWithTenantResolverCountsOnlyNonNilRegistrations` | migrated | none |
| `TestOptionWithTenantResolverFuncTypedNil` | migrated | none |
| `TestOptionWithTenantResolverNil` | migrated | none |
| `TestOptionWithTenantResolverNilAfterNonNil` | migrated | none |
| `TestOptionWithTenantResolverTypedNil` | migrated | none |
| `TestOptionWithTenantResolverTypedNilThenValid` | migrated | none |
| `TestOptionWithTenantResolverValidThenTypedNil` | migrated | none |
| `TestParseCommandReply` | migrated | none |
| `TestProjectionSupervisorContract` | migrated | none |
| `TestPublisherContractsAliasPortPublishing` | migrated | none |
| `TestRelocationDisabledByDefault` | migrated | none |
| `TestResolveLogger` | migrated | none |
| `TestRuntimeMovedTypesAreAliases` | migrated | none |
| `TestRuntimeSentinelsAreTheSameValues` | migrated | none |
| `TestSagaActionAndSagaCommandAreAliases` | migrated | none |
| `TestSpawnDependency` | migrated | none |
| `TestSpawnOption` | migrated | none |
| `TestTelemetryFields` | migrated | none |
| `TestToSpawnPlacement` | migrated | none |
| `TestToSupervisorDirective` | migrated | none |
| `TestToSupervisorDirectiveStop` | migrated | none |
| `TestTopicConstantsAreFixed` | migrated | none |
| `TestTopicConstantsAreNotPartitionedFormats` | migrated | none |
| `TestWithEventStream_NilKeepsTheDefault` | migrated | none |

#### `eventadapter`

| Test | Status | Dependencies to substitute |
|---|---|---|
| `TestChainAdapterReturnsError` | migrated | none |
| `TestChainAdapterUsesRevision` | migrated | none |
| `TestChainErrorStopsEarly` | migrated | none |
| `TestChainMixedNoopAndTransform` | migrated | none |
| `TestChainMultipleAdaptersAppliedInOrder` | migrated | none |
| `TestChainNoAdapters` | migrated | none |
| `TestChainNoopAdapter` | migrated | none |
| `TestChainSingleAdapterTransforms` | migrated | none |

#### `eventstream`

| Test | Status | Dependencies to substitute |
|---|---|---|
| `TestStream` | migrated | fixed wait |

#### `internal/engine/durablestate`

| Test | Status | Dependencies to substitute |
|---|---|---|
| `TestActorFallsBackToHandleCommandWithoutMetadata` | migrated | none |
| `TestDurableStateActorPersistStateAndPublishWritesTenantMetadata` | migrated | none |
| `TestDurableStateActorRecoverFromStoreSeedsActorTenant` | migrated | none |
| `TestDurableStateActorVerifyTenantForPersist` | migrated | none |
| `TestProvablyInSyncAfterConflict` | migrated | none |

#### `internal/engine/eventsource`

| Test | Status | Dependencies to substitute |
|---|---|---|
| `TestEventSourcedActorFallsBackToHandleCommandWithoutMetadata` | migrated | none |
| `TestEventSourcedActorMarshalEventWritesTenantMetadata` | migrated | none |
| `TestEventSourcedActorNewSnapshotEnvelopeWritesTenantMetadata` | migrated | none |
| `TestEventSourcedActorRecoverRejectsMismatchedSpawnBoundTenant` | migrated | none |
| `TestEventSourcedActorRecoverSeedsActorTenant` | migrated | none |
| `TestEventSourcedActorSeedActorTenant` | migrated | none |
| `TestEventSourcedActorVerifyTenantForPersist` | migrated | none |
| `TestResolveBatchPrecondition` | migrated | none |
| `TestRetryWithBackoff` | migrated | none |
| `TestShouldStayAliveAfterConflict` | migrated | none |

#### `internal/engine/protocol`

| Test | Status | Dependencies to substitute |
|---|---|---|
| `TestAnswerTenantBinding` | migrated | none |
| `TestAttachCarrier_RoundTrip` | migrated | none |
| `TestCarrierFromContext_NoneAttached` | migrated | none |
| `TestClassifierRegistrySentinelsDoNotPrefixEachOther` | migrated | none |
| `TestClassifyErrorReplyConcurrencyConflict` | migrated | none |
| `TestClassifyErrorReplyContextCanceled` | migrated | none |
| `TestClassifyErrorReplyDeadlineExceeded` | migrated | none |
| `TestClassifyErrorReplyDefaultsToFailed` | migrated | none |
| `TestClassifyErrorReplyWrappedConflictDegradesToFailed` | migrated | none |
| `TestMetadataFromContext_InvalidCarrierFailsClosed` | migrated | none |
| `TestMetadataFromContext_NoneAttached` | migrated | none |
| `TestMetadataFromContext_RematerializesMetadata` | migrated | none |
| `TestPreconditionFromRevisionMapsPerD4` | migrated | none |

#### `internal/engine/saga`

| Test | Status | Dependencies to substitute |
|---|---|---|
| `TestActorAttachCommandMetadata` | migrated | none |
| `TestSagaActionIsNoop` | migrated | none |
| `TestSagaActorBindOnFirstEvent` | migrated | none |
| `TestSagaActorCheckStateReadTenant` | migrated | none |
| `TestSagaActorEventContext` | migrated | none |
| `TestSagaActorPersistAndApplyEventsWritesTenantMetadata` | migrated | none |
| `TestSagaActorRecoverReplayTenantValidation` | migrated | none |
| `TestSagaStatusWireRoundTrip` | migrated | none |
| `TestSagaStatus_String` | migrated | none |

#### `internal/extensions`

| Test | Status | Dependencies to substitute |
|---|---|---|
| `TestDurableStateStore` | migrated | none |
| `TestEncryptorExtension` | migrated | none |
| `TestEntityConfig` | migrated | none |
| `TestEventAdapters` | migrated | none |
| `TestEventsStore` | migrated | none |
| `TestEventsStream` | migrated | none |
| `TestLocalBehavior` | migrated | none |
| `TestOffsetStore` | migrated | none |
| `TestProjectionExtension` | migrated | none |
| `TestSagaConfig` | migrated | none |
| `TestSnapshotStoreExt` | migrated | none |
| `TestTelemetryExtension` | migrated | none |
| `TestTenancyMarker` | migrated | none |

#### `internal/goaktlog`

| Test | Status | Dependencies to substitute |
|---|---|---|
| `TestBackend` | migrated | none |
| `TestBackendAttributesRecordsToItsDirectCaller` | migrated | none |
| `TestDiscardingBackendDisablesEveryLevel` | migrated | none |
| `TestGoaktArgsToMsg` | migrated | none |
| `TestGoaktToSlogLevel` | migrated | none |
| `TestLoggerAdapterAttributesRecordsToTheGoaktCallSite` | migrated | none |
| `TestLoggerAdapterFlush` | migrated | none |
| `TestLoggerAdapterFormattedMethodsSkipFormattingWhenDisabled` | migrated | none |
| `TestLoggerAdapterLevelTracksTheBackendAtRuntime` | migrated | none |
| `TestLoggerAdapterNonStringFirstArgumentBecomesTheMessage` | migrated | none |
| `TestLoggerAdapterRoutesEveryLevel` | migrated | none |
| `TestLoggerAdapterStdLogger` | migrated | none |
| `TestLoggerAdapterWithBuildsTheChildInTheBackend` | migrated | none |
| `TestLoggerWriterTrimsLineEndings` | migrated | none |
| `TestNewWrapsTheBackendInAnAdapter` | migrated | none |

#### `internal/instrumentation`

| Test | Status | Dependencies to substitute |
|---|---|---|
| `TestInstallPropagator` | migrated | none |
| `TestNewCreatesTheCatalog` | migrated | none |
| `TestNewWithoutMeterDisablesMetrics` | migrated | none |
| `TestNilInstrumentsRecordNothing` | migrated | none |
| `TestRecordingMethods` | migrated | none |
| `TestSendCommandSpan` | migrated | none |
| `TestShardRecordsTheGaugesWithProjectionAttributes` | migrated | none |
| `TestStartCommandSpan` | migrated | none |

#### `internal/logging`

| Test | Status | Dependencies to substitute |
|---|---|---|
| `TestDefaultLoggerIsKitLoggerGlobal` | migrated | none |
| `TestResolveLogger` | migrated | none |

#### `internal/projectionrunner`

| Test | Status | Dependencies to substitute |
|---|---|---|
| `TestOption` | migrated | none |
| `TestProjectionRunnerDefaultLogger` | migrated | none |
| `TestProjectionRunnerErrorPaths` | migrated | fixed wait |
| `TestProjectionRunnerFatalPaths` | migrated | fixed wait |
| `TestProjectionRunnerLagMetrics` | migrated | fixed wait |
| `TestRunner` | migrated | fixed wait |
| `TestRunnerPullEfficiency` | migrated | fixed wait |
| `TestStoreRetryDelay` | migrated | none |
| `TestWithDeadLetterHandler` | migrated | none |
| `TestWithDeadLetterHandlerNil` | migrated | none |
| `TestWithEncryptor` | migrated | none |
| `TestWithEventAdapters` | migrated | none |
| `TestWithEventAdaptersEmpty` | migrated | none |
| `TestWithMetrics` | migrated | none |

#### `internal/queue`

| Test | Status | Dependencies to substitute |
|---|---|---|
| `TestQueueDequeueEmpty` | migrated | none |
| `TestQueueIsEmpty` | migrated | none |
| `TestQueueLength` | migrated | none |

#### `internal/runner`

| Test | Status | Dependencies to substitute |
|---|---|---|
| `TestAddContextRunner` | migrated | none |
| `TestAddContextRunnerIf` | migrated | none |
| `TestChain` | migrated | none |

#### `internal/syncmap`

| Test | Status | Dependencies to substitute |
|---|---|---|
| `TestDelete` | migrated | none |
| `TestForEach` | migrated | none |
| `TestGet` | migrated | none |
| `TestLen` | migrated | none |
| `TestNewAndSet` | migrated | none |
| `TestReset` | migrated | none |
| `TestValues` | migrated | none |

#### `internal/testpb`

| Test | Status | Dependencies to substitute |
|---|---|---|
| `TestDescriptor_IsSoundAndCarriesTheModulePath` | migrated | none |

#### `internal/ticker`

| Test | Status | Dependencies to substitute |
|---|---|---|
| `TestTicker` | migrated | real timer |

#### `migration`

| Test | Status | Dependencies to substitute |
|---|---|---|
| `TestConsumeTag` | migrated | none |
| `TestConsumeVarint` | migrated | none |
| `TestExtractLegacyResultingState` | migrated | none |
| `TestMaxReplayLimitFitsInAnInt` | migrated | none |
| `TestMigratorOptions` | migrated | none |
| `TestMigratorReplaysSequencesBeyondTheLimitValue` | migrated | none |
| `TestMigratorRun` | migrated | none |
| `TestMigratorRunWithNilLogger` | migrated | none |
| `TestMigratorScope` | migrated | none |
| `TestMigratorUsesKitLogger` | migrated | none |
| `TestNewRejectsAnInvalidScope` | migrated | none |
| `TestNewTenantAdopter` | migrated | none |
| `TestNewTenantAdopterRejectsAnInvalidSourceScope` | migrated | none |
| `TestNewTenantAdopterRejectsZeroScanPageSize` | migrated | none |
| `TestNewTenantAdopterRequiresAFenceToWrite` | migrated | none |
| `TestScopedMigratorFailsClosedOnUnprovableTenantMetadata` | migrated | none |
| `TestTenantAdopterAcquiresFencesInDeterministicOrder` | migrated | none |
| `TestTenantAdopterAdoptsEveryAggregateAcrossMultiplePages` | migrated | none |
| `TestTenantAdopterAlreadyPresentInTargetIsNeverOverwritten` | migrated | none |
| `TestTenantAdopterAssignmentOkFalseLeavesUntouched` | migrated | none |
| `TestTenantAdopterChainedEventReceipts` | migrated | none |
| `TestTenantAdopterCountsSideEffectsOfAFailedAggregate` | migrated | none |
| `TestTenantAdopterDeletesSourceOfVerifiedExistingTarget` | migrated | none |
| `TestTenantAdopterDryRunWritesNothing` | migrated | none |
| `TestTenantAdopterDurableStateTargetRaceIsStoppedByItsPrecondition` | migrated | none |
| `TestTenantAdopterEventsVerificationCatchesCorruptedWrite` | migrated | none |
| `TestTenantAdopterEventsVerificationRejectsDuplicateSequenceRows` | migrated | none |
| `TestTenantAdopterExplicitPersistenceIDsForDurableStateOnly` | migrated | none |
| `TestTenantAdopterFencedSourceWriterCannotInterleaveWithDeletion` | migrated | none |
| `TestTenantAdopterLaterSameTenantTargetIsNotEquivalent` | migrated | none |
| `TestTenantAdopterMissingSourceClassification` | migrated | none |
| `TestTenantAdopterNeverOverwritesAConcurrentlyCreatedTargetSnapshot` | migrated | none |
| `TestTenantAdopterNeverReportsDeletionOfASourceThatStillExists` | migrated | none |
| `TestTenantAdopterPerAggregateFailureDoesNotAbortRun` | migrated | none |
| `TestTenantAdopterPreDeleteCheckIgnoresReplayOrder` | migrated | none |
| `TestTenantAdopterReRunIsANoOp` | migrated | none |
| `TestTenantAdopterRealRunCopiesAndKeepsSource` | migrated | none |
| `TestTenantAdopterReceiptProvesAdoptionAfterSourceDeletion` | migrated | none |
| `TestTenantAdopterRefusesDeletionOfReplacedSameSequenceSnapshot` | migrated | none |
| `TestTenantAdopterRefusesDeletionOfRewrittenSourceEvent` | migrated | none |
| `TestTenantAdopterRejectsATargetEqualToTheSource` | migrated | none |
| `TestTenantAdopterReleasesItsFencesOnEveryPath` | migrated | none |
| `TestTenantAdopterReplaysSequencesBeyondTheLimitValue` | migrated | none |
| `TestTenantAdopterSamePositionTargetClassification` | migrated | none |
| `TestTenantAdopterSnapshotDeletionRefusesSuccessUnderConcurrentWrites` | migrated | none |
| `TestTenantAdopterSnapshotOnlyReRunAfterDeletionIsIdempotent` | migrated | none |
| `TestTenantAdopterSnapshotVerificationCatchesCorruptedWrite` | migrated | none |
| `TestTenantAdopterSourceDeletingReRunIsIdempotent` | migrated | none |
| `TestTenantAdopterSourceDeletionOnlyAfterVerification` | migrated | none |
| `TestTenantAdopterSourceDeletionRefusesSuccessUnderConcurrentWrites` | migrated | none |
| `TestTenantAdopterStampsTargetTenantMetadata` | migrated | none |
| `TestTenantAdopterStateVerificationCatchesCorruptedWrite` | migrated | none |
| `TestTenantAdopterTargetExtendedByLiveWritesIsAlreadyPresent` | migrated | none |
| `TestTenantAdopterTwoTenantsAreIsolated` | migrated | none |

#### `persistence`

| Test | Status | Dependencies to substitute |
|---|---|---|
| `TestConflictErrorActualRevisionUnknownWhenNotSupplied` | migrated | none |
| `TestConflictErrorDoesNotMatchUnrelatedSentinel` | migrated | none |
| `TestConflictErrorErrorMessageCanonicalGrammar` | migrated | none |
| `TestConflictErrorIdentifiableViaErrorsAs` | migrated | none |
| `TestConflictErrorIdentifiableViaErrorsIs` | migrated | none |
| `TestConflictErrorScopeAccessor` | migrated | none |
| `TestConflictErrorWrappedIsStillIdentifiable` | migrated | none |
| `TestNewTenantScopeRejectsEmptyTenantID` | migrated | none |
| `TestParseConflictErrorIsExactInverseOfError` | migrated | none |
| `TestParseConflictErrorRejectsMalformedMessages` | migrated | none |
| `TestParseConflictErrorRoundTripsAdversarialIdentifiers` | migrated | none |
| `TestScopeIsUnscoped` | migrated | none |
| `TestScopeNewTenantScopeIsValid` | migrated | none |
| `TestScopeStringDistinguishesKinds` | migrated | none |
| `TestScopeTenantIDRoundTrips` | migrated | none |
| `TestScopeTenantNamedUnscopedDoesNotEqualUnscoped` | migrated | none |
| `TestScopeTwoTenantScopesWithDifferentIDsAreNotEqual` | migrated | none |
| `TestScopeTwoTenantScopesWithSameIDAreEqual` | migrated | none |
| `TestScopeUnscopedIsValid` | migrated | none |
| `TestScopeUnscopedNotEqualToTenantScope` | migrated | none |
| `TestScopeZeroValueIsInvalid` | migrated | none |
| `TestWritePreconditionComparable` | migrated | none |
| `TestWritePreconditionExpectGenesis` | migrated | none |
| `TestWritePreconditionExpectRevision` | migrated | none |
| `TestWritePreconditionExpectRevisionZeroIsNotGenesisOrUnconditional` | migrated | none |
| `TestWritePreconditionUnconditional` | migrated | none |
| `TestWritePreconditionZeroValueIsInvalid` | migrated | none |

#### `port/adapter`

| Test | Status | Dependencies to substitute |
|---|---|---|
| `TestAccessors_AreIndependent` | migrated | none |
| `TestAccessors_ImplementingValueIsReturned` | migrated | none |
| `TestAccessors_TypedNilIsTreatedAsAbsent` | migrated | none |
| `TestAccessors_UndeclaredValueReturnsZeroAndFalse` | migrated | none |
| `TestAssertionSitesNegativeControl` | migrated | none |
| `TestDescriptor_DeclaresAndServes` | migrated | none |
| `TestLifecycleCapabilities` | migrated | none |

#### `port/adapter/adaptertest`

| Test | Status | Dependencies to substitute |
|---|---|---|
| `TestCapture_CapabilityWithoutCheckFailsAT1` | migrated | none |
| `TestCapture_CloseAfterFailedStartFailsAT3` | migrated | none |
| `TestCapture_CloseIgnoringTheDeadlineFailsAT4` | migrated | none |
| `TestCapture_ConstructorAcquireIsNotExercised` | migrated | none |
| `TestCapture_DeclaredReadyWithoutPingFailsAT1` | migrated | none |
| `TestCapture_EmptyNameFailsAT1` | migrated | none |
| `TestCapture_FailStartWhoseAcquireSucceedsFailsAT2` | migrated | none |
| `TestCapture_FailingPingFailsAT5` | migrated | none |
| `TestCapture_ImpliedCapReadyIsNotRequiredForStores` | migrated | none |
| `TestCapture_InvalidTargetFails` | migrated | none |
| `TestCapture_LyingDescriptorFailsAT1NamingCapStart` | migrated | none |
| `TestCapture_NonIdempotentCloseFailsAT3` | migrated | none |
| `TestCapture_OnlyErrUnreachableSkips` | migrated | none |
| `TestCapture_TargetCapabilitiesAreCheckedBothWays` | migrated | none |
| `TestCapture_TargetCapabilitiesMayNotListSuiteCapabilities` | migrated | none |
| `TestCapture_UndeclaredAdapterIsNotExercisedByAT1` | migrated | none |
| `TestCapture_UndeclaredCapabilityFailsAT1` | migrated | none |
| `TestCapture_UnstableDescriptorFailsAT1` | migrated | none |
| `TestCapture_WrongPortFailsAT1` | migrated | none |
| `TestImpliesReadyMatchesTheStorePortConstants` | migrated | none |
| `TestRun_CorrectBorrowedStorePasses` | migrated | none |
| `TestRun_CorrectStarterPassesEveryCheck` | migrated | none |

#### `port/behavior`

| Test | Status | Dependencies to substitute |
|---|---|---|
| `TestDomainOnlyBehaviorsRunThroughTheContracts` | migrated | none |

#### `port/publishing/publishingtest`

| Test | Status | Dependencies to substitute |
|---|---|---|
| `TestCapture_LostEventFailsPT3` | migrated | none |
| `TestCapture_NoObserverIsNotExercised` | migrated | none |
| `TestCapture_OnlyUnreachableSkips` | migrated | none |
| `TestCapture_PublishingAfterCloseFailsPT1` | migrated | none |
| `TestCapture_RealAdaptertestErrUnreachableSkips` | migrated | none |
| `TestCapture_UnstableIDFailsPT2` | migrated | none |
| `TestRunEvents_CorrectPublisherPasses` | migrated | none |
| `TestRunState_CorrectPublisherPasses` | migrated | none |

#### `port/runtime`

| Test | Status | Dependencies to substitute |
|---|---|---|
| `TestAdapterSettingLastWins` | migrated | none |
| `TestAdapterSettingLookupWithNonComparableKeyIsAbsent` | migrated | none |
| `TestAdapterSettingNilValueIsStored` | migrated | none |
| `TestAdapterSettingRoundTrip` | migrated | none |
| `TestDoubleSendCommandRunsTheBehavior` | migrated | none |
| `TestDoubleSpawnAppliesOptionsInOrderAndSkipsNil` | migrated | none |
| `TestDoubleSpawnResolvesDocumentedDefaults` | migrated | none |
| `TestDoubleUnsupportedOperations` | migrated | none |
| `TestEnumValuesAreUnchanged` | migrated | none |
| `TestErrUnsupportedWrapsStandardError` | migrated | none |
| `TestResolveSpawnOptionsAppliesInOrder` | migrated | none |
| `TestResolveSpawnOptionsDefaults` | migrated | none |
| `TestResolveSpawnOptionsEachOptionReachesItsGetter` | migrated | none |
| `TestResolveSpawnOptionsEmbeddedOptionApplies` | migrated | none |
| `TestResolveSpawnOptionsNonNilWrapperOfNilOptionPanics` | migrated | none |
| `TestResolveSpawnOptionsSkipsNilOption` | migrated | none |
| `TestSagaStatusString` | migrated | none |
| `TestSentinelMessagesAreKept` | migrated | none |
| `TestSpawnSettingsIsolatedFromLaterResolutions` | migrated | none |
| `TestUnsupportedError` | migrated | none |
| `TestWithAdapterSettingPanicsAtBuildTime` | migrated | none |

#### `projection`

| Test | Status | Dependencies to substitute |
|---|---|---|
| `TestDiscardDeadLetterHandler_Handle` | migrated | none |
| `TestDiscardDeadLetterHandler_InterfaceCompliance` | migrated | none |
| `TestDiscardHandler_Handle` | migrated | none |
| `TestDiscardHandler_InterfaceCompliance` | migrated | none |
| `TestNewDiscardDeadLetterHandler` | migrated | none |
| `TestNewDiscardHandler` | migrated | none |
| `TestNewRecovery_Defaults` | migrated | none |
| `TestNewRecovery_WithAllOptions` | migrated | none |
| `TestRecoveryOption` | migrated | none |

#### `tenancy`

| Test | Status | Dependencies to substitute |
|---|---|---|
| `TestAdministrative_WithCorrelationIDIsOptional` | migrated | none |
| `TestAsFixedTenantResolver` | migrated | none |
| `TestAttach_BindsTenantContextRetrievableViaFrom` | migrated | none |
| `TestAttach_IsIdempotentForTheSameTenantContext` | migrated | none |
| `TestAttach_RejectsChangingAlreadyBoundTenantContext` | migrated | none |
| `TestAttach_RejectsZeroValueTenantContext` | migrated | none |
| `TestAttach_ValidAdministrativeContextStillFlowsThroughUnchanged` | migrated | none |
| `TestAttach_ValidTenantScopedContextStillFlowsThroughUnchanged` | migrated | none |
| `TestCapFixedTenant_IsAnUntypedConstant` | migrated | none |
| `TestError_ErrorMessage` | migrated | none |
| `TestError_IsDoesNotMatchOtherSentinels` | migrated | none |
| `TestError_IsMatchesSentinelByReason` | migrated | none |
| `TestError_ReasonAccessor` | migrated | none |
| `TestError_TenantAccessorWithAttribution` | migrated | none |
| `TestError_TenantAccessorWithoutAttribution` | migrated | none |
| `TestError_UnwrapReturnsCause` | migrated | none |
| `TestError_UnwrapReturnsNilWithoutCause` | migrated | none |
| `TestFixedTenantAccessors_NeverResolve` | migrated | none |
| `TestFixedTenantOf` | migrated | none |
| `TestFrom_ReturnsFalseWhenNothingAttached` | migrated | none |
| `TestInvocation_EntrypointResolvesAndAttaches_BehaviorOnlyRequires` | migrated | none |
| `TestInvocation_SkippingEntrypointAttachMeansBehaviorFails` | migrated | none |
| `TestMarshalMetadata_AdministrativeScope_OmitsCorrelationIDWhenAbsent` | migrated | none |
| `TestMarshalMetadata_AdministrativeScope_UsesEgoTenantKeys` | migrated | none |
| `TestMarshalMetadata_TenantScope_UsesEgoTenantKeys` | migrated | none |
| `TestMetadata_RoundTrip_AdministrativeScope` | migrated | none |
| `TestMetadata_RoundTrip_TenantScope` | migrated | none |
| `TestNewAdministrativeContext_IsTypeDistinctAndAttributed` | migrated | none |
| `TestNewAdministrativeContext_RejectsZeroValueAdministrative` | migrated | none |
| `TestNewAdministrative_AcceptsActorAndReason` | migrated | none |
| `TestNewAdministrative_RequiresActorAndReason` | migrated | none |
| `TestNewTenantContext_DifferentTenantsProduceDifferentContexts` | migrated | none |
| `TestNewTenantContext_ProducesTenantScopedContext` | migrated | none |
| `TestNewTenantContext_RejectsEmptyTenantID` | migrated | none |
| `TestNewTenantContext_RejectsZeroValueTenantID` | migrated | none |
| `TestNewTenantContext_RevalidatesTenantIDBypassingConstructor` | migrated | none |
| `TestNewTenantID_AcceptsArbitraryNonUUIDIdentifiers` | migrated | none |
| `TestNewTenantID_AcceptsInteriorWhitespace` | migrated | none |
| `TestNewTenantID_AcceptsMaxLength` | migrated | none |
| `TestNewTenantID_DoesNotNormalizeCase` | migrated | none |
| `TestNewTenantID_RejectsControlRune` | migrated | none |
| `TestNewTenantID_RejectsEmpty` | migrated | none |
| `TestNewTenantID_RejectsInvalidUTF8` | migrated | none |
| `TestNewTenantID_RejectsLeadingOrTrailingWhitespace` | migrated | none |
| `TestNewTenantID_RejectsTabAndNewline` | migrated | none |
| `TestNewTenantID_RejectsTooLong` | migrated | none |
| `TestNewTenantID_RejectsWhitespaceOnly` | migrated | none |
| `TestRequire_AcceptsValidTenantContextBoundDirectly` | migrated | none |
| `TestRequire_RejectsInvalidTenantContextEvenIfSomehowBound` | migrated | none |
| `TestRequire_ReturnsBoundTenantContext` | migrated | none |
| `TestRequire_ReturnsErrMissingWhenNothingAttached` | migrated | none |
| `TestSagaBoundary_ReconstructsTenantIdentityFromCarriedMetadata` | migrated | none |
| `TestSagaBoundary_SkippingMetadataReconstructionFailsClosed` | migrated | none |
| `TestSentinels_AreDistinctFromEachOther` | migrated | none |
| `TestTenantContext_ZeroValueIsNeitherScope` | migrated | none |
| `TestUnmarshalMetadata_RejectsAdministrativeScopeMissingAttribution` | migrated | none |
| `TestUnmarshalMetadata_RejectsMissingScope` | migrated | none |
| `TestUnmarshalMetadata_RejectsTenantScopeWithInvalidID` | migrated | none |
| `TestUnmarshalMetadata_RejectsUnrecognizedScope` | migrated | none |
| `TestVerifyUnchanged_ReturnsErrDeniedWhenDifferent` | migrated | none |
| `TestVerifyUnchanged_ReturnsNilWhenEqual` | migrated | none |
| `TestWithSingleTenant_IgnoresIncomingContext` | migrated | none |
| `TestWithSingleTenant_IndistinguishableFromAnyResolver` | migrated | none |
| `TestWithSingleTenant_ProducesTenantScopedContext` | migrated | none |
| `TestWithSingleTenant_RejectsInvalidTenantID` | migrated | none |

#### `testkit`

| Test | Status | Dependencies to substitute |
|---|---|---|
| `TestConformanceCatchesNonIsolatingStore` | migrated | none |
| `TestDurableStateScenario_GivenStateWhenCommandThenStateAndVersion` | migrated | none |
| `TestDurableStateScenario_UnhandledCommandReturnsError` | migrated | none |
| `TestDurableStateScenario_WhenCommandFromInitialState` | migrated | none |
| `TestDurableStateScenario_WhenCommandReturnsError` | migrated | none |
| `TestDurableStoreConformance` | migrated | none |
| `TestDurableStore_CheckPreconditionsAloneDoesNotPreventStateStoreConflict` | migrated | none |
| `TestDurableStore_Connect` | migrated | none |
| `TestDurableStore_Disconnect` | migrated | none |
| `TestDurableStore_GetLatestState_NotConnected` | migrated | none |
| `TestDurableStore_InvalidScopeRejected` | migrated | none |
| `TestDurableStore_NewDurableStore` | migrated | none |
| `TestDurableStore_Ping` | migrated | none |
| `TestDurableStore_T10_ConcurrentGenesisHasExactlyOneWinner` | migrated | none |
| `TestDurableStore_T9_ConcurrentExpectRevisionHasExactlyOneWinner` | migrated | none |
| `TestDurableStore_WriteAndGetState` | migrated | none |
| `TestDurableStore_WriteState_ExactRevisionSucceedsWhenCurrent` | migrated | none |
| `TestDurableStore_WriteState_GenesisConflictsOnExisting` | migrated | none |
| `TestDurableStore_WriteState_GenesisSucceedsOnEmpty` | migrated | none |
| `TestDurableStore_WriteState_InvalidPreconditionIsRejected` | migrated | none |
| `TestDurableStore_WriteState_NotConnected` | migrated | none |
| `TestDurableStore_WriteState_StaleRevisionIsConflict` | migrated | none |
| `TestDurableStore_WriteState_UnconditionalIsLegacyBehavior` | migrated | none |
| `TestEventSourcedScenario_GivenEventsApplyOnTopOfGivenState` | migrated | none |
| `TestEventSourcedScenario_GivenEventsBuildTheState` | migrated | none |
| `TestEventSourcedScenario_GivenEventsFailureIsReportedAsArrangementFailure` | migrated | none |
| `TestEventSourcedScenario_GivenStateIsPassedToCommandHandler` | migrated | none |
| `TestEventSourcedScenario_GivenStateWhenCommandThenEventsAndState` | migrated | none |
| `TestEventSourcedScenario_HandleEventFailsOnProducedEvent` | migrated | none |
| `TestEventSourcedScenario_UnhandledCommandReturnsError` | migrated | none |
| `TestEventSourcedScenario_WhenCommandFromInitialState` | migrated | none |
| `TestEventSourcedScenario_WhenCommandProducesNoEvents` | migrated | none |
| `TestEventSourcedScenario_WhenCommandReturnsError` | migrated | none |
| `TestEventStoreConformance` | migrated | none |
| `TestEventStoreReplayEventsAcceptsTheMigrationReplayBounds` | migrated | none |
| `TestEventStore_Connect` | migrated | none |
| `TestEventStore_DeleteEvents` | migrated | none |
| `TestEventStore_Disconnect` | migrated | none |
| `TestEventStore_GetLatestEvent` | migrated | none |
| `TestEventStore_GetShardEvents` | migrated | none |
| `TestEventStore_InvalidScopeRejected` | migrated | none |
| `TestEventStore_NewEventsStore` | migrated | none |
| `TestEventStore_PersistenceIDs` | migrated | none |
| `TestEventStore_PersistenceIDsPaginationExhaustive` | migrated | none |
| `TestEventStore_Ping` | migrated | none |
| `TestEventStore_ShardOffsets` | migrated | none |
| `TestEventStore_T10_ConcurrentGenesisHasExactlyOneWinner` | migrated | none |
| `TestEventStore_T8_ConcurrentExpectRevisionHasExactlyOneWinner` | migrated | none |
| `TestEventStore_T8_ConcurrentExpectRevisionHoldsAcrossManyAggregates` | migrated | none |
| `TestEventStore_UnscopedDoesNotCollideWithTenantScope` | migrated | none |
| `TestEventStore_WriteAndReplayEvents` | migrated | none |
| `TestEventStore_WriteEvents_ConditionalBatchMustShareOnePersistenceID` | migrated | none |
| `TestEventStore_WriteEvents_DuplicateSequenceNumberDoesNotDuplicateShardEventsOrOffsets` | migrated | none |
| `TestEventStore_WriteEvents_DuplicateSequenceNumberOverwritesNotAccumulates` | migrated | none |
| `TestEventStore_WriteEvents_DuplicateSequenceNumberThenDeleteEventsLeavesNoResidual` | migrated | none |
| `TestEventStore_WriteEvents_DuplicateSequenceNumberWithinConditionalWriteOverwrites` | migrated | none |
| `TestEventStore_WriteEvents_ExactRevisionSucceedsWhenCurrent` | migrated | none |
| `TestEventStore_WriteEvents_GenesisConflictsOnExisting` | migrated | none |
| `TestEventStore_WriteEvents_GenesisSucceedsOnEmpty` | migrated | none |
| `TestEventStore_WriteEvents_InvalidPreconditionIsRejected` | migrated | none |
| `TestEventStore_WriteEvents_StaleRevisionIsConflict` | migrated | none |
| `TestEventStore_WriteEvents_UnconditionalIsLegacyBehavior` | migrated | none |
| `TestKeyStore_DeleteKey` | migrated | none |
| `TestKeyStore_GetKey` | migrated | none |
| `TestKeyStore_GetOrCreateKey` | migrated | none |
| `TestKeyStore_NewKeyStore` | migrated | none |
| `TestOffsetStore_Connect` | migrated | none |
| `TestOffsetStore_Disconnect` | migrated | none |
| `TestOffsetStore_NewOffsetStore` | migrated | none |
| `TestOffsetStore_Ping` | migrated | none |
| `TestOffsetStore_WriteAndGetOffset` | migrated | none |
| `TestSnapshotStoreConformance` | migrated | none |
| `TestSnapshotStore_Connect` | migrated | none |
| `TestSnapshotStore_DeleteSnapshots` | migrated | none |
| `TestSnapshotStore_Disconnect` | migrated | none |
| `TestSnapshotStore_InvalidScopeRejected` | migrated | none |
| `TestSnapshotStore_NewSnapshotStore` | migrated | none |
| `TestSnapshotStore_Ping` | migrated | none |
| `TestSnapshotStore_WriteAndGetSnapshot` | migrated | none |
| `TestStoreDescriptors` | migrated | none |
| `TestStoresAdapterConformance` | migrated | none |

### Module `publisher/kafka`

#### `publisher/kafka`

| Test | Status | Dependencies to substitute |
|---|---|---|
| `TestClosureGuardRejectsCompositionRoot` | migrated | none |
| `TestPublishBeforeStartMatchesPublishingSentinel` | migrated | none |

### Module `publisher/nats`

#### `publisher/nats`

| Test | Status | Dependencies to substitute |
|---|---|---|
| `TestClosureGuardRejectsCompositionRoot` | migrated | none |
| `TestPublishBeforeStartMatchesPublishingSentinel` | migrated | none |

### Module `publisher/pulsar`

#### `publisher/pulsar`

| Test | Status | Dependencies to substitute |
|---|---|---|
| `TestClosureGuardRejectsCompositionRoot` | migrated | none |
| `TestPublishBeforeStartMatchesPublishingSentinel` | migrated | none |

### Module `publisher/websocket`

#### `publisher/websocket`

| Test | Status | Dependencies to substitute |
|---|---|---|
| `TestClosureGuardRejectsCompositionRoot` | migrated | none |
| `TestDescriptors` | migrated | none |

### Module `test/compat`

#### `test/compat`

| Test | Status | Dependencies to substitute |
|---|---|---|
| `TestUrdSentinelIsThePublishingSentinel` | migrated | none |

## Out of phase

256 tests need a real component or resource, so they are not part of this phase and keep their current execution. They are the documented exceptions of the unit gate: a test file that starts a real goakt actor system, runs `go list`, opens an `httptest` server or a loopback socket, or reaches the Postgres example is listed, one line and one reason each, in `.github/unit-test-gate-resources.txt`, which the gate checks in `-strict` mode so a line that no longer applies fails CI. Each one is listed with the real dependency that keeps it out. The 19 Postgres tests used to skip without a DSN; they now live in `inttest/flows/eventstore` and start their own container (#210). Twenty-five of these tests (12 in `engine`, 13 in `compose/goakt`) look like unit tests but start an actor system through a helper or through `App.Start`.

### Module `.`

- `compose/goakt` (15)
  - starts a GoAkt cluster on loopback ports: `TestCluster_AppTwoNodePlacesAndStopsCleanly`
  - starts an actor system: `TestNew_G2_ActorSystemName`
  - starts an actor system through a helper: `TestApp_ValidSpecRunsAnEngine`, `TestEngine_UndeclaredFamilyReturnsTypedError`, `TestRuntime_ConsumerDrivesTheAppEndToEnd`, `TestRuntime_IsTheEngineAfterStartAndAfterStop`, `TestRuntime_NilAfterFailedStart`, `TestStart_ActorSystemStepFailsForReal`, `TestStart_AttachStepStartsAndProbesPublishersFirst`, `TestStart_FailureAtEachStepReleasesEverything`, `TestStart_PublisherFailureAtK`, `TestStart_PublisherPingFailureNamesTheAdapter`, `TestStop_AfterStopIsNoOp`, `TestStop_D7OpenQuestion_StateFlushedDuringActorShutdown`, `TestStop_OrderMatchesD7`
- `engine` (143)
  - runs `go list`: `TestArchitectureCommand`, `TestArchitectureTenancy`
  - reads the repository source files: `TestArchitectureKitLoggerIsTheOnlyLoggingBackend`
  - starts a GoAkt cluster on loopback ports: `TestClusterEngineSingleNodeServesProjectionsAndEntities`, `TestClusterEngineStartProjectionAlreadyExists`, `TestClusterEngineNeutralBehaviors`, `TestClusterEngineRemoteEntitySpawn`, `TestClusterEngineRejectsUnplaceableBehaviors`, `TestClusterEngineRemoteSpawnTenantBinding`, `TestClusterEventPublisherHighPartitionCount`, `TestClusterNewEngineRejectsValueTypeKind`
  - starts an actor system: `TestAddPublishersRejectsDuplicateIDs`, `TestAdministrativeScopeIsNeverAnAggregateTenantScope`, `TestBatchedPreconditionMatrix_GenesisBase`, `TestConfigGoaktOptionsEncryptor`, `TestConfigGoaktOptionsNoTenancyMarkerWithTypedNilResolver`, `TestConfigGoaktOptionsNoTenancyMarkerWithoutResolver`, `TestConfigGoaktOptionsProjectionDefaultsRecovery`, `TestConfigGoaktOptionsTelemetry`, `TestConfigGoaktOptionsTenancyMarker`, `TestDurableStateActorDiscardsHandlerOutputAfterDeadlineExpiry`, `TestDurableStateCheckPreconditionsPassesYetExpectedRevisionConflicts`, `TestDurableStateConcurrentGenesisWritersYieldExactlyOneCommit`, `TestDurableStateConditionalWriteEvaluatedAgainstStorageRevision`, `TestDurableStateConflictResultShape`, `TestDurableStateExpectedRevisionEndToEndPropagation`, `TestDurableStateExpectedRevisionExactMatchCommits`, `TestDurableStateHandlerShapeUnchangedByExpectedRevision`, `TestDurableStateNoPartialCommitOnConflict`, `TestDurableStateNonAdjacentVersionIsNeverConcurrencyConflict`, `TestDurableStateStaleExpectedRevisionRejectedAtStoreNotCache`, `TestEngineActorSystemAccessor`, `TestEngineAddEventPublishers`, `TestEngineAddEventPublishersGuards`, `TestEngineAddStatePublishers`, `TestEngineAddStatePublishersGuards`, `TestEngineCommandRejectsTenantMismatchWithSpawnDeclaredTenant`, `TestEngineConfigRegistersAllExtensions`, `TestEngineDispatchClampsTimeoutToDeadline`, `TestEngineDispatchDispatchesHandleEnvelope`, `TestEngineDispatchEffectiveDeadlinePrecedence`, `TestEngineDispatchRejectsExpiredDeadlineWithoutInvokingHandler`, `TestEngineDispatchRejectsInvalidMetadataWithoutInvokingHandler`, `TestEngineDispatchRejectsZeroValueEnvelopeWithoutPanicking`, `TestEngineDurableState`, `TestEngineDurableStateRequiresStateStore`, `TestEngineEntityExists`, `TestEngineEntitySpawnRequiresExplicitTenantWhenResolverHasNoFixedTenant`, `TestEngineEntitySpawnWithExplicitTenantResolvesOnce`, `TestEngineEntitySpawnWithoutResolverStaysUnscoped`, `TestEngineEntityValueTypeBehaviorSingleNode`, `TestEngineEntityWithRetentionPolicy`, `TestEngineEraseEntity`, `TestEngineEraseEntityCannotEraseAnotherTenantsRecord`, `TestEngineEraseEntityErrors`, `TestEngineEventPublisherKeepsGoingOnPublishError`, `TestEngineEventSourced`, `TestEngineIsProjectionRunningActorOfError`, `TestEngineNotStartedGuardsDirect`, `TestEngineProjection`, `TestEngineProjectionLagClampsNegative`, `TestEngineProjectionLagErrors`, `TestEngineProjectionLagHappyPath`, `TestEngineProjectionLagWithEvents`, `TestEngineProjectionsOwnHandlers`, `TestEnginePublisherIdleCPU`, `TestEngineRebuildProjectionErrors`, `TestEngineRebuildProjectionRemoveError`, `TestEngineRebuildProjectionResetOffsetError`, `TestEngineRebuildProjectionRestartError`, `TestEngineRebuildProjectionSuccess`, `TestEngineRejectsNilBehaviorsSingleNode`, `TestEngineRespawnInLegacyModeIsUnchanged`, `TestEngineSagaHappyPath`, `TestEngineSagaSpawnError`, `TestEngineSagaStatusErrorPaths`, `TestEngineSagaStatusMapsWireStatus`, `TestEngineSagaStatusReportsLifecycleStatus`, `TestEngineSagaStatusTenantIsolation`, `TestEngineSendCommandDispatchesDurableStateHandleEnvelope`, `TestEngineSendCommandDispatchesHandleEnvelope`, `TestEngineSendCommandErrors`, `TestEngineSendCommandUnexpectedReply`, `TestEngineSendCommandWithTelemetry`, `TestEngineSpawnMethodsDomainOnlySingleNode`, `TestEngineSpawnWithMultiTenantFixedTenantResolverNeedsWithTenant`, `TestEngineSpawnsDomainOnlyBehaviorsSingleNode`, `TestEngineStartProjectionNotRegistered`, `TestEngineStartProjectionStandaloneSpawnError`, `TestEngineStartWithTelemetry`, `TestEngineStatePublisherKeepsGoingOnPublishError`, `TestEngineStopAttemptsEveryStep`, `TestEngineStopReturnsEventPublisherCloseError`, `TestEngineStopReturnsStatePublisherCloseError`, `TestEngineSubscribeBeforeStart`, `TestEngineSubscribeReceivesEventsAndStates`, `TestEngineWithSingleTenantSpawnNeedsNoWithTenant`, `TestEventPayloadCarriesShard`, `TestEventPublisherFanOutToMultipleSubscribers`, `TestEventPublisherReceivesEventsFromEntity`, `TestEventSourcedActorBatchPathDoesNotContaminateBatchStateAfterDeadlineExpiry`, `TestEventSourcedActorDirectPathDiscardsHandlerOutputAfterDeadlineExpiry`, `TestEventSourcedActorStaysConsistentAfterConflict`, `TestEventSourcedBatchedExpectedRevisionSuccessAndConflict`, `TestEventSourcedExpectedRevisionGenesisConflictsOnExistingAggregate`, `TestEventSourcedExpectedRevisionGenesisSucceedsOnNewAggregate`, `TestEventSourcedExpectedRevisionPropagatesToPersistencePrecondition`, `TestEventSourcedExpectedRevisionStaleIsConcurrencyConflict`, `TestEventSourcedExpectedRevisionSuccessMatchesCurrent`, `TestEventSourcedHandlerArgumentsNeverCarryExpectedRevision`, `TestEventSourcedIntegrationConcurrentGenesisYieldsExactlyOneCommit`, `TestEventSourcedIntegrationExactRevisionCommitsAndAdvancesStore`, `TestEventSourcedIntegrationStaleRevisionRejectedStoreUnchanged`, `TestEventSourcedLegacyCommandIsUnconditional`, `TestGoaktOptionsCarryTheResolvedLogger`, `TestLegacyCompatEventSourcedAndDurableStateNeverConflict`, `TestNewEngineAcceptsTypedNilPointerKind`, `TestNewEngineRejectsUnregistrableKindsSingleNode`, `TestNewEngineTenantResolverValidation`, `TestNewEngineValidation`, `TestProjectionActorRunnerFailure`, `TestSendCommandResolverSwapIdenticalSequence`, `TestSendCommandSingleTenantZeroPlumbing`, `TestSendCommandTenantResolution`, `TestSpawnWithoutEventsStore`, `TestStatePublisherReceivesDurableStateUpdates`, `TestTelemetryContract`, `TestTelemetryDisabled`, `TestTenantWritePathE2E`, `TestWithEntityKindsAndWithBehaviorKindsShareRegistration`, `TestWithEventStream_UsesTheGivenStream`
  - starts an actor system through a helper: `TestBatchAdmissionGateRejectsStaleRevision_ForcesEarlyFlushThenFoundsFreshBatch`, `TestBatchedExternalWriterWinsCAS_RejectsWholeBatchWithoutAdvancingCounter`, `TestBatchedPhysicalBaseAnchorsToPreBatchRevision_NotLogicalCounter`, `TestBatchedZeroEventAdmittedCommandStillPreservesLaterPrecondition`, `TestBatchedZeroEventFounderNeverOpensBatch`, `TestDispatchRejectsTenantBindingQuery`, `TestEngineConcurrentCrossTenantSpawnHasExactlyOneWinner`, `TestEngineRespawnUnderAnotherTenantIsRejected`, `TestWithEntityFamilies_Combined`, `TestWithEntityFamilies_NotDeclaredAllowsEveryFamily`, `TestWithEntityFamilies_UndeclaredFamilyIsRejected`, `TestWithEntityFamilies_UnknownBitsAreIgnored`
- `internal/engine/durablestate` (9)
  - starts an actor system: `TestDurableStateActorFailedFirstCommandDoesNotAppropriateActor`, `TestDurableStateActorGetStateCommandTenancyGate`, `TestDurableStateActorPostStopTenantPersist`, `TestDurableStateActorPreStartExtensions`, `TestDurableStateActorProcessCommandRejectsCrossTenant`, `TestDurableStateActorRecoverFromStoreLegacyVersionZeroGenesis`, `TestDurableStateActorTenancyGate`, `TestDurableStateActorTenancyWritePath`, `TestDurableStateBehavior`
- `internal/engine/eventsource` (23)
  - starts an actor system: `TestEventSourcedActor`, `TestEventSourcedActorBatch`, `TestEventSourcedActorBatchTenantHomogeneity`, `TestEventSourcedActorBatchTenantHomogeneity_ZeroEventCrossTenant`, `TestEventSourcedActorBatchTenantHomogeneity_ZeroEventSameTenant`, `TestEventSourcedActorErrorPaths`, `TestEventSourcedActorGetStateCommandRejectsCrossTenant`, `TestEventSourcedActorGetStateCommandRequiresTenantWhenTenantAware`, `TestEventSourcedActorGetStateDuringPersist`, `TestEventSourcedActorLegacyModeAlwaysUsesUnscopedStore`, `TestEventSourcedActorPreStartFailsClosedWithoutTenantScope`, `TestEventSourcedActorProcessCommandAndReplyRejectsCrossTenant`, `TestEventSourcedActorResetBatchDoesNotClearActorTenant`, `TestEventSourcedActorSpawnBindsExactTenantScope`, `TestEventSourcedActorTenancyGate`, `TestEventSourcedActorTenantIdentitySurvivesRestart`, `TestEventWriteObservableSequence`, `TestEventsJanitorActor`, `TestEventsWriterActor`, `TestSnapshotAndRetentionObservableSequence`, `TestSnapshotsWriterActor`, `TestSnapshotsWriterContract`, `TestWriterContract`
- `internal/engine/projection` (2)
  - starts an actor system: `TestProjection`, `TestProjectionActorPreStartFailure`
- `internal/engine/saga` (5)
  - starts an actor system: `TestSagaActor`, `TestSagaActorCompensateUsesBoundTenant`, `TestSagaActorDurableTenantBinding`, `TestSagaActorSendCommandThreadsTenantContext`, `TestSagaFailsClosed`
- `internal/extensions` (2)
  - starts an actor system: `TestOptionalExtension`, `TestRequireExtension`
- `internal/instrumentation` (1)
  - runs `go list`: `TestArchitectureInstrumentationStaysRuntimeNeutral`
- `internal/logging` (1)
  - runs `go list`: `TestArchitectureLoggingStaysRuntimeNeutral`
- `internal/projectionrunner` (1)
  - runs `go list`: `TestArchitectureProjectionRunnerStaysRuntimeNeutral`
- `internal/runtimeconsumer` (1)
  - runs `go list`: `TestArchitectureRuntimeConsumerProductionClosureExcludesRootAndGoAkt`
- `migration` (3)
  - runs `go list`: `TestArchitectureMigrationProductionClosureExcludesRootAndGoAkt`
  - starts an actor system: `TestScopedMigratorSnapshotRecoversThroughTenantAwareActor`, `TestTenantAdopterEndToEndRecoveryThroughRealActor`
- `port/adapter` (5)
  - parses the repository source files: `TestArchitectureNoPrivateCopiesOfOptionalInterfaces`, `TestArchitectureOptionalInterfacesAreAssertedOnlyInTheirAccessors`, `TestArchitecturePortNameConstantsAreUntyped`
  - runs `go list`: `TestArchitectureAdapterDependsOnlyOnStdlib`, `TestArchitectureContractPackagesDoNotImportAdapter`
- `port/adapter/adaptertest` (1)
  - runs `go list`: `TestArchitectureAdaptertestDependsOnlyOnStdlibAndAdapter`
- `port/behavior` (1)
  - runs `go list`: `TestArchitectureBehaviorDependsOnlyOnContracts`
- `port/publishing` (1)
  - runs `go list`: `TestArchitecturePublishingDependsOnlyOnContracts`
- `port/publishing/publishingtest` (1)
  - runs `go list`: `TestArchitecturePublishingtestDependsOnlyOnStdlibPublishingAndEgopb`
- `port/runtime` (2)
  - runs `go list`: `TestArchitectureRuntimeDependsOnlyOnContracts`, `TestArchitectureRuntimeTestClosureExcludesGoAktAndRoot`

### Module `example/cluster`

- `example/cluster` (30)
  - Postgres, at the time of the inventory (since moved to `inttest/flows/eventstore`, #210): `TestPostgresEventStore_ConcurrentExpectRevisionHasExactlyOneWinner`, `TestPostgresEventStore_Conformance`, `TestPostgresEventStore_DeleteEventsLocksRevisionAgainstConcurrentWrite`, `TestPostgresEventStore_DeleteKeepsRevisionPerTenant`, `TestPostgresEventStore_ExpectGenesisConflictsOnExistingID`, `TestPostgresEventStore_PartialDeleteKeepsRevision`, `TestPostgresEventStore_PersistenceIDs_ZeroPageSize_WithData`, `TestPostgresEventStore_SchemaMigratesLegacyDatabase`, `TestPostgresEventStore_SchemaMigratesLegacyTenantMetadata`, `TestPostgresEventStore_TenantMetadataAbsent_ReadsAsNone`, `TestPostgresEventStore_TenantMetadataRoundTrips_ConditionalWrite`, `TestPostgresEventStore_TenantMetadataRoundTrips_GetShardEvents`, `TestPostgresEventStore_TenantMetadataRoundTrips_UnconditionalWrite`, `TestPostgresEventStore_TenantScopeIsolatesRecords`, `TestPostgresEventStore_TotalDeleteKeepsRevision`, `TestPostgresEventStore_UnconditionalMixedBatchesDoNotDeadlock`, `TestPostgresEventStore_UnconditionalRaceDistinctSequenceConflict`, `TestPostgresEventStore_UnconditionalRaceSameSequenceConflict`, `TestPostgresEventStore_UnconditionalWriteCannotBreakExpectRevision`
  - tests the example program in example/cluster or benchmark: `TestPostgresEventStore_DeleteEvents_InvalidScope`, `TestPostgresEventStore_GetLatestEvent_InvalidScope`, `TestPostgresEventStore_ImplementsEventsStore`, `TestPostgresEventStore_PersistenceIDs_InvalidScope`, `TestPostgresEventStore_PersistenceIDs_ZeroPageSize`, `TestPostgresEventStore_ReplayEvents_InvalidScope`, `TestPostgresEventStore_WriteEvents_EmptyBatchConditional`, `TestPostgresEventStore_WriteEvents_EmptyBatchUnconditionalSucceeds`, `TestPostgresEventStore_WriteEvents_InvalidPrecondition`, `TestPostgresEventStore_WriteEvents_InvalidScope`, `TestPostgresEventStore_WriteEvents_MixedIDBatchConditional`

### Module `publisher/kafka`

- `publisher/kafka` (1)
  - runs `go list`: `TestArchitectureUnitTestClosureExcludesRuntimeAndRoot`

### Module `publisher/nats`

- `publisher/nats` (1)
  - runs `go list`: `TestArchitectureUnitTestClosureExcludesRuntimeAndRoot`

### Module `publisher/pulsar`

- `publisher/pulsar` (1)
  - runs `go list`: `TestArchitectureUnitTestClosureExcludesRuntimeAndRoot`

### Module `publisher/websocket`

- `publisher/websocket` (6)
  - runs `go list`: `TestArchitectureUnitTestClosureExcludesRuntimeAndRoot`
  - runs a local HTTP test server: `TestCloseIsIdempotent`, `TestDurableStatePublisherAdapterConformance`, `TestDurableStatePublisherPublishingConformance`, `TestEventsPublisherAdapterConformance`, `TestEventsPublisherPublishingConformance`

