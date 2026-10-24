FROM --platform=$BUILDPLATFORM golang:1.27.3-alpine3.23@sha256:754d5200fc3edae76d3b6598a9a3fac7e74981ee6a5413ab955025f185753ef2 AS go
FROM --platform=$BUILDPLATFORM node:24.18.1-alpine3.23@sha256:c2cc26d8f991c2db236ad51a61efee843c482372d6d22570787309d511694110 AS build
COPY --from=go /usr/local/go /usr/local/go
ENV PATH="/usr/local/go/bin:${PATH}" CGO_ENABLED=0 GOPATH=/go
WORKDIR /src
COPY package.json pnpm-lock.yaml pnpm-workspace.yaml ./
COPY apps/platform/package.json apps/platform/package.json
COPY apps/control-plane-worker/package.json apps/control-plane-worker/package.json
COPY contracts/platform/package.json contracts/platform/package.json
COPY experiments/cloudflare-hosting/package.json experiments/cloudflare-hosting/package.json
COPY experiments/canvas-editor/package.json experiments/canvas-editor/package.json
RUN npm install --global pnpm@11.17.0 && pnpm install --frozen-lockfile
COPY . .
ARG TARGETOS
ARG TARGETARCH
ARG SOURCE_REVISION=development
RUN --mount=type=cache,target=/go/pkg/mod --mount=type=cache,target=/root/.cache/go-build GOOS="$TARGETOS" GOARCH="$TARGETARCH" SOURCE_REVISION="$SOURCE_REVISION" pnpm app:build /out/astilba

FROM alpine:3.23.4@sha256:5b10f432ef3da1b8d4c7eb6c487f2f5a8f096bc91145e68878dd4a5019afde11
ARG SOURCE_REVISION=development
LABEL org.opencontainers.image.source="https://github.com/astilbahq/astilba" \
      org.opencontainers.image.licenses="AGPL-3.0-only" \
      org.opencontainers.image.revision="$SOURCE_REVISION"
COPY --from=build /out/astilba /usr/local/bin/astilba
COPY --from=build /src/apps/platform/dist/source /usr/share/licenses/astilba
COPY --from=go /etc/ssl/certs/ca-certificates.crt /etc/ssl/certs/ca-certificates.crt
USER 65532:65532
EXPOSE 8080
ENTRYPOINT ["/usr/local/bin/astilba"]
CMD ["serve"]
