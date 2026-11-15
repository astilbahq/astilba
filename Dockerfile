FROM --platform=$BUILDPLATFORM golang:1.27.4-alpine3.23@sha256:146e3b9c7c1e2ec3d3f5348c8a686151a75e1976e50efac9fc022ebe446a9a82 AS go
FROM --platform=$BUILDPLATFORM node:24.18.1-alpine3.23@sha256:c2cc26d8f991c2db236ad51a61efee843c482372d6d22570787309d511694110 AS build
COPY --from=go /usr/local/go /usr/local/go
ENV PATH="/usr/local/go/bin:${PATH}" CGO_ENABLED=0 GOPATH=/go
WORKDIR /src
COPY package.json pnpm-lock.yaml pnpm-workspace.yaml ./
COPY apps/platform/package.json apps/platform/panda.config.ts apps/platform/
COPY apps/platform/theme/ apps/platform/theme/
COPY contracts/platform/package.json contracts/platform/package.json
COPY experiments/canvas-editor/package.json experiments/canvas-editor/panda.config.ts experiments/canvas-editor/
COPY scripts/fetch-attempts.mjs scripts/install-pnpm.mjs scripts/
# Node's fetch ignores HTTPS_PROXY unless told otherwise; npm and pnpm do not.
RUN NODE_USE_ENV_PROXY=1 node scripts/install-pnpm.mjs && pnpm install --frozen-lockfile
COPY . .
ARG TARGETOS
ARG TARGETARCH
ARG SOURCE_REVISION=development
RUN --mount=type=cache,target=/go/pkg/mod --mount=type=cache,target=/root/.cache/go-build GOOS="$TARGETOS" GOARCH="$TARGETARCH" SOURCE_REVISION="$SOURCE_REVISION" pnpm app:build /out/astilba

FROM alpine:3.24.4@sha256:f59abe656b692d901e184be3445e084786602108599d2a51d229c0f5a48c71bf
ARG SOURCE_REVISION=development
LABEL org.opencontainers.image.source="https://github.com/astilbahq/astilba" \
      org.opencontainers.image.licenses="AGPL-3.0-only" \
      org.opencontainers.image.revision="$SOURCE_REVISION"
COPY --from=build /out/astilba /usr/local/bin/astilba
COPY --from=build /src/apps/platform/dist/source /usr/share/licenses/astilba
USER 65532:65532
EXPOSE 8080
ENTRYPOINT ["/usr/local/bin/astilba"]
CMD ["serve"]
