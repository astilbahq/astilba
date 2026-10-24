.PHONY: check verify verify-host verify-build verify-git verify-canvas check-app verify-app verify-image verify-agent-host

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
	cd experiments/durable-execution && sudo env PATH=/opt/astilba-plat4/go/bin:/usr/bin:/bin CGO_ENABLED=1 PLAT4_HOST_TEST=1 PLAT4_ARCH="$(PLAT4_ARCH)" /opt/astilba-plat4/go/bin/go test -race -count=1 -timeout=8m -v -run '^TestHostRuntime$$' .

verify-build:
	cd experiments/durable-execution && sudo env PATH=/opt/astilba-plat4/go/bin:/usr/bin:/bin CGO_ENABLED=1 PLAT4_HOST_TEST=1 PLAT4_ARCH="$(PLAT4_ARCH)" PLAT5_HOST_TEST=1 /opt/astilba-plat4/go/bin/go test -race -count=1 -timeout=12m -v -run '^Test(BuildArtifacts|PostgresRestore)$$' .

check-app:
	@for file in scripts/platform-*.mjs; do node --check "$$file" || exit 1; done
	node --test scripts/platform-proxy-test.mjs
	cd services/platform && test -z "$$(gofmt -l .)" && go mod verify && go vet ./... && go test -short ./...
	pnpm generate:check
	pnpm --filter @astilba/dashboard check
	pnpm --filter @astilba/control-plane-worker check

verify-app: check-app
	pnpm agent:build
	cd services/platform && go test -race -count=1 -timeout=5m ./...
	pnpm --filter @astilba/dashboard test:browser
	pnpm --filter @astilba/dashboard exec wrangler deploy --dry-run --outdir ../../.artifacts/p1/worker
	pnpm --filter @astilba/control-plane-worker verify

verify-image:
	pnpm app:image-test

verify-agent-host:
	cd services/platform && sudo env PATH=/opt/astilba-plat4/go/bin:/usr/bin:/bin CGO_ENABLED=1 PLAT_APP_DOCKER_HOST=unix:///run/astilba-plat4/docker.sock ASTILBA_NATIVE_AGENT=1 /opt/astilba-plat4/go/bin/go test -race -count=1 -timeout=5m -v -run '^TestNativeAgentService$$' ./internal/integration
	cd services/platform && sudo env PATH=/opt/astilba-plat4/go/bin:/usr/bin:/bin CGO_ENABLED=1 PLAT_APP_DOCKER_HOST=unix:///run/astilba-plat4/docker.sock ASTILBA_NATIVE_AGENT=1 /opt/astilba-plat4/go/bin/go test -race -count=1 -timeout=15m -v -run '^TestNative((Visual|Compose)Deployment|Pipeline|Upgrade)$$' ./internal/agent
