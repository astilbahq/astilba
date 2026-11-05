.PHONY: check verify verify-host verify-build verify-git verify-canvas check-app verify-app verify-app-core verify-app-build verify-app-go-remaining verify-app-go-crosshost verify-app-go-preview verify-image verify-agent-host verify-agent-host-core verify-agent-host-recovery verify-agent-host-visual verify-agent-host-upgrade verify-troubleshooting-soak verify-agent-colima check-ci check-hooks check-source-size check-inline-sql check-vulnerabilities

APP_COORDINATION_TESTS = ^(TestCrossHostCoordinatedPlacement|TestCrossHostSubscriptionExpiry)$$
APP_PREVIEW_RECOVERY_TESTS = ^TestPreviewRemovalSurvivesCheckpointUnion$$
NATIVE_GO = sudo --preserve-env=GOCACHE,GOMODCACHE env PATH=/opt/astilba-plat4/go/bin:/opt/astilba-plat4/bin:/usr/sbin:/usr/bin:/sbin:/bin CGO_ENABLED=1
NATIVE_AGENT_GO = $(NATIVE_GO) PLAT_APP_DOCKER_HOST=unix:///run/astilba-plat4/docker.sock ASTILBA_NATIVE_AGENT=1 /opt/astilba-plat4/go/bin/go test -race -count=1 -v

check:
	$(MAKE) check-app
	cd experiments/durable-execution && test -z "$$(gofmt -l .)" && go mod verify && go vet ./...
	cd experiments/git-compose && test -z "$$(gofmt -l .)" && go mod verify && go vet ./...

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
	node --test scripts/ci-plan.test.mjs scripts/native-lanes.test.mjs scripts/vulnerability-scan.test.mjs
	node scripts/native-lanes.mjs

check-vulnerabilities:
	node scripts/vulnerability-scan.mjs

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
	node --test scripts/platform-generate-test.mjs
	cd services/platform && test -z "$$(gofmt -l .)" && go mod verify && go vet ./... && go tool staticcheck ./... && go test -short ./...
	pnpm generate:check
	pnpm --filter @astilba/dashboard check

verify-app: verify-app-core
	pnpm --filter @astilba/dashboard test:browser --project=chromium
	pnpm --filter @astilba/dashboard test:browser --project=webkit
	pnpm --filter @astilba/dashboard storybook:test

verify-app-core: verify-app-build
	$(MAKE) -j3 verify-app-go-remaining verify-app-go-crosshost verify-app-go-preview

verify-app-build: check-app
	pnpm agent:build
	pnpm cli:build
	pnpm --filter @astilba/dashboard build

verify-app-go-remaining:
	cd services/platform && go test -race -count=1 -timeout=15m -skip '$(APP_COORDINATION_TESTS)|$(APP_PREVIEW_RECOVERY_TESTS)' ./...

verify-app-go-crosshost:
	cd services/platform && go test -race -count=1 -timeout=15m -run '$(APP_COORDINATION_TESTS)' ./internal/integration

verify-app-go-preview:
	cd services/platform && go test -race -count=1 -timeout=15m -run '$(APP_PREVIEW_RECOVERY_TESTS)' ./internal/integration

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
	cd services/platform && $(NATIVE_AGENT_GO) -timeout=30m -run '^TestNative((Compose|CrossHost)Deployment|Pipeline|ReleaseCommandTransport|PreviewEnvironment|TroubleshootingTarget|TroubleshootingSession|TroubleshootingShellExit|TroubleshootingIdleLimit|TroubleshootingRefusals|TroubleshootingCrashCleanup|TroubleshootingEndsSurvivors|TroubleshootingStalledInput|TroubleshootingNoShell|TroubleshootingShellChoice)$$' ./internal/agent
	cd services/platform && $(NATIVE_AGENT_GO) -timeout=10m -run '^TestNativeTroubleshooting(Service|Proxies)$$' ./internal/integration

verify-agent-host-recovery:
	cd services/platform && $(NATIVE_AGENT_GO) -timeout=15m -run '^TestNative(AgentService|TroubleshootingEndToEnd|CrossHostOlderBackupRecovery|CrossHostDrainedRecovery|CrossHostUpgradeRecovery)$$' ./internal/integration

verify-agent-host-visual:
	cd services/platform && $(NATIVE_AGENT_GO) -timeout=30m -run '^TestNativeVisualDeployment$$' ./internal/agent

verify-agent-host-upgrade:
	cd services/platform && $(NATIVE_AGENT_GO) -timeout=30m -run '^TestNativeSupportedMajorUpgrades$$' ./internal/agent

# The real idle limit and maximum through every proxy, which takes more than 75 minutes.
verify-troubleshooting-soak:
	cd services/platform && $(NATIVE_GO) PLAT_APP_DOCKER_HOST=unix:///run/astilba-plat4/docker.sock ASTILBA_NATIVE_AGENT=1 ASTILBA_NATIVE_SOAK=1 /opt/astilba-plat4/go/bin/go test -race -count=1 -v -parallel=3 -timeout=100m -run '^TestTroubleshootingProxySoak$$' ./internal/integration

verify-agent-colima:
	bash scripts/native-agent-colima.sh '$(value PLAT_AGENT_COLIMA_PROFILE)' '$(value PLAT_AGENT_TESTS)'
