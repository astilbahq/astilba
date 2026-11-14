FROM golang:1.27.4-alpine3.24@sha256:0a5f7fe796599ff487605987432e7619ae45beeb2fba4c2ce0b907879932aebf AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY main.go ./
ARG FAIL_BUILD=false
RUN test "$FAIL_BUILD" != true && CGO_ENABLED=0 go build -trimpath -ldflags='-s -w' -o /app .

FROM busybox:1.37.0@sha256:bdf57e528e45e4433820e045b29b4597825a1c9e38353532d90a01445013f82e
COPY --from=build /app /app
USER 65532:65532
EXPOSE 8080
HEALTHCHECK --interval=2s --timeout=2s --start-period=5s --retries=5 CMD wget -q -O /dev/null http://127.0.0.1:8080/ready || exit 1
ENTRYPOINT ["/app"]
