.PHONY: check verify verify-host verify-build verify-git verify-canvas check-app verify-app verify-app-core verify-app-build verify-app-go-remaining verify-app-go-operations verify-app-go-crosshost verify-app-go-preview verify-app-go-network verify-app-go-m-to-z verify-app-go-revoked-host verify-image verify-agent-host verify-agent-host-core verify-agent-host-recovery verify-agent-host-visual verify-agent-host-upgrade verify-troubleshooting-soak verify-agent-colima check-ci check-hooks check-source-size check-inline-sql check-vulnerabilities report-pins check-lane-budget dev dev-wipe

APP_COORDINATION_TESTS = ^TestCrossHostCoordinatedPlacement$$
APP_PREVIEW_RECOVERY_TESTS = ^TestPreviewRemovalSurvivesCheckpointUnion$$
APP_OPERATIONS_TESTS = ^Test(Troubleshooting|ManagedNetwork|Maintenance|Automation|Git)
APP_NETWORK_TESTS = ^(TestAgentCannotSpendNetworkGeneration|TestRevocationSurvivesSpentGeneration|TestReceiptClaimsStayWithinMargin|TestRecoveryRefusesGenerationsNeverIssued|TestRecoveryKeepsRestoredGenerationHistory|TestUnaccountedReceiptDoesNotConflictWithItsHostsExport|TestRestoredManagementRecoversAHostAhead|TestHostileClaimsAfterRestoreMoveNothing|TestUnfencedHostAheadRecoversThroughFencing|TestRefusedRemovalFallsBackToTheWholeManifest|TestReAddedMemberReceivesItsWholeManifest|TestSweepWaitsForAManifestOnItsWay|TestRevokedMemberHoldsTheNetworkOnlyThroughItsPeers|TestRevokedMemberReadsPendingWhileAnythingIs|TestRenewedMemberWaitsForItsHostUntilRevokedAgain|TestRemovedMemberHoldsTheNetworkUntilItAnswersOrIsRevoked|TestImportedRevokedMemberWaitsOnlyForTheRemainingHosts|TestRestoredRevokedMemberWaitsOnlyForTheRemainingHosts|TestRecoveryNeedsNothingFromARevokedServer|TestRecoveryRefusesAnotherMachinesExportForARevokedServer|TestRecoveryIgnoresARevokedServersExportedOutcome|TestRouteConflictHoldsAMemberOnlyForItsGeneration|TestRepeatedRouteConflictsHoldBackOnlyTheRefusingHost|TestRevocationReachesARefusingHostWithNoRecord|TestMisdescribingHostLimitsOnlyItsOwnRemoval|TestReplayedDescriptionDoesNotSpendTheNetworkGeneration|TestRemovingAMemberThatNeverJoinedReleasesTheNetwork|TestAcknowledgedRemovalCannotStandInForJoining|TestUnpreparedHostIsHeldUntilItReportsReady|TestMemberWithAnotherMembersKeyIsAskedAgainOnlyWhenTheNetworkChanges|TestLongOperationHistoryKeepsCrossHostDeploymentAndExport|TestRecoveryReadsABaseCapturedBeforeHistoryWasBounded|TestDeploymentHistoryStillReachesTheArchiveLimit|TestCrossHostHistoryGzipCouldNotSealStillDeploys|TestUnpreparedHoldDoesNotDependOnTheNote|TestMemberNotedByTheKeyMigrationIdentifiesAgain|TestReceiptlessNetworkFailureWaitsForTheWorkspaceLock|TestRecoveryReleasesNetworkSweepHolds)$$
APP_REVOKED_HOST_TESTS = ^(TestEnvironmentMovesOffARevokedSecondary|TestEnvironmentConsolidatesOffARevokedSecondary|TestRevocationReleasesAHeldDeployment|TestDeploymentFinishesWithoutAHostRevokedAfterPublication|TestRevokingAPrimaryReleasesItsSecondaries|TestRevocationEndsStandaloneWorkForGood|TestRevokedConsumerStopsABackupBeforeItStarts|TestRevokedPrimaryResumesItsConsumers|TestRevokedPrimaryEndsAnUpgradeWaitingForTheOwner|TestRevocationStopsAReleaseCommandThatHasNotStarted|TestRefreshShowsARevokedHostAsUnknown|TestBackupSurvivesLosingAConsumerAfterIt|TestRevocationDuringCheckpointClosesIt|TestRevokedPrimaryEndsACancelledUpgrade|TestPlanLeavingAServerBehindNeedsItStillRevoked|TestRevokedHostWithNothingLeftLosesItsRuntimeRecord|TestRecoveryKeepsWorkARevocationSettled|TestRevocationReleasesAHostHeldInPreparation|TestPublishedDeploymentSurvivesLosingItsPrimaryBeforeTheNextPass|TestRevokedConsumerStopsABackupNotYetSent|TestReplacingARevokedPrimaryStartsItsVolumesEmpty|TestSecondaryTakesOverFromARevokedPrimary|TestReplacementCanBeRetriedOnTheSameServer|TestReplacementNeedsThePrimaryStillRevoked|TestReplacementPreparesTheNewPrimarysIngress|TestReplacementLeavesALoneIngressAlone|TestReplacementMovesOnWhenItsTargetIsRevokedToo|TestReplacementKeepsADeclaredVolumeNothingMounts|TestReplacementOntoAFormerSecondaryKeepsOneUnit|TestStaleReplacementDoesNotUndoANewerOne|TestRecoveryKeepsAReplacedPrimaryDetached|TestRecoveryContinuesWithoutARevokedSecondary|TestRecoveryIgnoresWhatARevokedParticipantClaims|TestRecoveryIgnoresWhatARevokedHostStillRuns|TestRecoveryRestoresARevokedPrimarysRootFromManagement|TestRecoveryExplainsALostRevokedPrimaryRoot|TestRecoveryUsesARevokedPrimarysCapsulesManagementLacks|TestRecoveryExtendsARevokedPrimarysRootOnlyAsManagementSealedIt|TestRecoveryFitsAParticipantsProofOfProgressBeyondTheBackup|TestRecoveryResumesAnInterruptedReplacement)$$
APP_FAMILY_TESTS = $(APP_COORDINATION_TESTS)|$(APP_PREVIEW_RECOVERY_TESTS)|$(APP_OPERATIONS_TESTS)|$(APP_NETWORK_TESTS)|$(APP_REVOKED_HOST_TESTS)
APP_M_TO_Z_TESTS = ^Test[M-Z]
NATIVE_GO = sudo --preserve-env=GOCACHE,GOMODCACHE env PATH=/opt/astilba-plat4/go/bin:/opt/astilba-plat4/bin:/usr/sbin:/usr/bin:/sbin:/bin CGO_ENABLED=1
NATIVE_AGENT_GO = $(NATIVE_GO) PLAT_APP_DOCKER_HOST=unix:///run/astilba-plat4/docker.sock ASTILBA_NATIVE_AGENT=1 /opt/astilba-plat4/go/bin/go test -race -count=1 -v

check:
	$(MAKE) check-app
	cd experiments/durable-execution && test -z "$$(gofmt -l .)" && go mod verify && go vet ./...
	cd experiments/git-compose && test -z "$$(gofmt -l .)" && go mod verify && go vet ./...
	cd experiments/fleet-capacity && test -z "$$(gofmt -l .)" && go mod verify && go vet ./...

verify:
	bash experiments/durable-execution/verify.sh
	bash experiments/git-compose/verify.sh
	$(MAKE) verify-canvas
	$(MAKE) verify-app

verify-canvas:
	pnpm --filter @astilba/canvas-spike verify

verify-git:
	bash experiments/git-compose/verify.sh

verify-host:
	cd experiments/durable-execution && sudo --preserve-env=GOCACHE,GOMODCACHE env PATH=/opt/astilba-plat4/go/bin:/usr/bin:/bin CGO_ENABLED=1 PLAT4_HOST_TEST=1 PLAT4_ARCH="$(PLAT4_ARCH)" /opt/astilba-plat4/go/bin/go test -race -count=1 -timeout=8m -v -run '^TestHostRuntime$$' .

verify-build:
	cd experiments/durable-execution && sudo --preserve-env=GOCACHE,GOMODCACHE env PATH=/opt/astilba-plat4/go/bin:/usr/bin:/bin CGO_ENABLED=1 PLAT4_HOST_TEST=1 PLAT4_ARCH="$(PLAT4_ARCH)" PLAT5_HOST_TEST=1 /opt/astilba-plat4/go/bin/go test -race -count=1 -timeout=12m -v -run '^Test(BuildArtifacts|PostgresRestore)$$' .

check-ci:
	node --test scripts/ci-plan.test.mjs scripts/native-lanes.test.mjs scripts/app-lanes.test.mjs scripts/lane-budget.test.mjs scripts/vulnerability-scan.test.mjs scripts/pin-report.test.mjs scripts/pin-sources.test.mjs scripts/report-issue.test.mjs scripts/scheduled-report.test.mjs scripts/schedule-watch.test.mjs scripts/fetch-attempts.test.mjs scripts/install-pnpm.test.mjs scripts/pin-alignment.test.mjs scripts/go-release-age.test.mjs scripts/npm-release-changes.test.mjs scripts/public-suffix-list.test.mjs
	node scripts/native-lanes.mjs
	node scripts/app-lanes.mjs
	node scripts/pin-alignment.mjs

# Both run, so a finding cannot hide a failing Public Suffix List check.
check-vulnerabilities:
	status=0; node scripts/vulnerability-scan.mjs || status=1; node scripts/public-suffix-list.mjs --check || status=1; exit $$status

report-pins:
	node scripts/pin-report.mjs

check-lane-budget:
	node scripts/lane-budget.mjs

check-hooks:
	node --test .claude/hooks/hooks.test.mjs

check-source-size:
	node --test scripts/source-size.test.mjs
	node scripts/source-size.mjs

check-inline-sql:
	node --test scripts/inline-sql.test.mjs
	node scripts/inline-sql.mjs

check-app: check-ci check-hooks check-source-size check-inline-sql
	@for file in scripts/platform-*.mjs; do node --check "$$file" || exit 1; done
	node --test scripts/platform-generate-test.mjs scripts/platform-dev.test.mjs scripts/test-ports.test.mjs
	cd services/platform && test -z "$$(gofmt -l .)" && go mod verify && go vet ./... && go tool staticcheck ./... && go run ./internal/lint/astilba-lint -test=false ./... && go run ./internal/lint/astilba-lint -jsonguard -jsontag ./... && go test -short ./...
	pnpm generate:check
	pnpm --filter @astilba/dashboard check

verify-app: verify-app-core
	pnpm --filter @astilba/dashboard test:browser --project=chromium
	pnpm --filter @astilba/dashboard test:browser --project=webkit
	pnpm --filter @astilba/dashboard storybook:test

verify-app-core: verify-app-build
	$(MAKE) -j7 verify-app-go-remaining verify-app-go-m-to-z verify-app-go-operations verify-app-go-crosshost verify-app-go-preview verify-app-go-network verify-app-go-revoked-host

verify-app-build: check-app
	pnpm agent:build
	pnpm cli:build
	pnpm --filter @astilba/dashboard build

verify-app-go-remaining:
	cd services/platform && go test -race -count=1 -v -timeout=15m -skip '$(APP_FAMILY_TESTS)|$(APP_M_TO_Z_TESTS)' ./...

# These select across every package, because the -skip above also reaches every package.
verify-app-go-m-to-z:
	cd services/platform && go test -race -count=1 -v -timeout=15m -run '$(APP_M_TO_Z_TESTS)' -skip '$(APP_FAMILY_TESTS)' ./...

verify-app-go-operations:
	cd services/platform && go test -race -count=1 -v -timeout=15m -run '$(APP_OPERATIONS_TESTS)' ./...

verify-app-go-crosshost:
	cd services/platform && go test -race -count=1 -v -timeout=15m -run '$(APP_COORDINATION_TESTS)' ./internal/integration

verify-app-go-preview:
	cd services/platform && go test -race -count=1 -v -timeout=15m -run '$(APP_PREVIEW_RECOVERY_TESTS)' ./internal/integration

verify-app-go-network:
	cd services/platform && go test -race -count=1 -v -timeout=15m -run '$(APP_NETWORK_TESTS)' ./internal/integration

verify-app-go-revoked-host:
	cd services/platform && go test -race -count=1 -v -timeout=15m -run '$(APP_REVOKED_HOST_TESTS)' ./internal/integration

verify-image:
	pnpm app:image-test

# The native suites publish the same ingress ports, so they run one at a time on a host.
verify-agent-host:
	$(MAKE) verify-agent-host-recovery
	$(MAKE) verify-agent-host-core
	$(MAKE) verify-agent-host-visual
	$(MAKE) verify-agent-host-upgrade

verify-agent-host-core:
	cd services/platform && $(NATIVE_GO) ASTILBA_NATIVE_NETWORK=1 /opt/astilba-plat4/go/bin/go test -race -count=1 -timeout=5m -v -run '^TestNativeManagedLinks$$' ./internal/hostnetwork
	cd services/platform && $(NATIVE_AGENT_GO) -timeout=30m -run '^TestNative((Compose|CrossHost)Deployment|CrossHostTakeover|Pipeline|PullRefusesBlobsUnlikeTheirDescriptors|PullWatchStopsAPullBeyondItsMeasure|PullPutsAMultiPlatformImageInDocker|ComposePullPolicyKeepsRunningContainers|BuilderConfinement|BuildNetwork|ReleaseCommandTransport|RouteBackendConversion|LargeEnvironmentsLeaveRoomForOthers|FailedPublishKeepsOtherTraffic|UpgradedHostKeepsItsRoutes|FailedReconciliationReplacesAChangedProxy|InterruptedHostChangeRecreatesTheProxyOnce|FailedPublishRestoresTheProxyGateway|FailedReleaseKeepsAChangedProxyUntrusted|TakeoverMovesTheProxyOntoTheGateway|TakeoverLeavesAReadyProxyRunning|TakeoverWithoutRoutesStartsAPortlessProxy|FailedTakeoverKeepsThePreviousRoutes|InterruptedTakeoverIsPutBack|TakeoverOnAFullHostChangesNothing|CompletionWaitsForAWritersHealthCheck|BackupOfStoppedWriter|RefusedReservationOfStoppedWriterCompletes|BackupOfWriterFailingItsHealthCheck|BackupOfWriterFailingItsReadiness|ReservationRefusesChangedConfiguration|PauseRefusesAWriterStoppedSinceReserving|WriterAlreadyFailingThatCannotRestart|AcceptingAWriterThatCannotStart|AcceptingAWriterThatDoesNotComeBack|LateCompletionReleasesTheReservation|LateCompletionKeepsAnUnconfirmedReservation|LateRestoreLeavesTheSourceToResume|LateRetirementAndCompletionReleaseTheReservation|LatePublicationLeavesTheHostToAbort|PreviewEnvironment|TroubleshootingTarget|TroubleshootingSession|TroubleshootingShellExit|TroubleshootingIdleLimit|TroubleshootingRefusals|TroubleshootingCrashCleanup|TroubleshootingEndsSurvivors|TroubleshootingStalledInput|TroubleshootingNoShell|TroubleshootingShellChoice)$$' ./internal/agent
	cd services/platform && $(NATIVE_AGENT_GO) -timeout=10m -run '^TestNativeTroubleshooting(Service|Proxies)$$' ./internal/integration

verify-agent-host-recovery:
	cd services/platform && $(NATIVE_AGENT_GO) -timeout=15m -run '^TestNative(AgentService|TroubleshootingEndToEnd|CrossHostOlderBackupRecovery|CrossHostDrainedRecovery|CrossHostUpgradeRecovery)$$' ./internal/integration

verify-agent-host-visual:
	cd services/platform && $(NATIVE_AGENT_GO) -timeout=30m -run '^TestNative(VisualDeployment|HostRunsFiftyRoutedEnvironments|OwnerPoolHoldsManagedNetworks|FullHostRefusesAnotherEnvironment)$$' ./internal/agent

verify-agent-host-upgrade:
	cd services/platform && $(NATIVE_AGENT_GO) -timeout=30m -run '^TestNativeSupportedMajorUpgrades$$' ./internal/agent

# The real idle limit and maximum through every proxy, which takes more than 75 minutes.
verify-troubleshooting-soak:
	cd services/platform && $(NATIVE_GO) PLAT_APP_DOCKER_HOST=unix:///run/astilba-plat4/docker.sock ASTILBA_NATIVE_AGENT=1 ASTILBA_NATIVE_SOAK=1 /opt/astilba-plat4/go/bin/go test -race -count=1 -v -parallel=3 -timeout=100m -run '^TestTroubleshootingProxySoak$$' ./internal/integration

verify-agent-colima:
	bash scripts/native-agent-colima.sh '$(value PLAT_AGENT_COLIMA_PROFILE)' '$(value PLAT_AGENT_TESTS)'

# The local stack: management, dashboard and an enrolled agent in a disposable Colima VM.
# See docs/local-development.md.
PLAT_DEV_COLIMA_PROFILE ?= astilba-dev

dev:
	pnpm dev --vm '$(value PLAT_DEV_COLIMA_PROFILE)'

dev-wipe:
	pnpm dev --vm '$(value PLAT_DEV_COLIMA_PROFILE)' --wipe
