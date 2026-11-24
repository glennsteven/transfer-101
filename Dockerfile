FROM golang:1.26.10-bookworm AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY cmd/ ./cmd/
COPY internal/ ./internal/
RUN CGO_ENABLED=0 go build -mod=readonly -trimpath -o /out/ \
    ./cmd/api ./cmd/worker ./cmd/mockbank ./cmd/funds ./cmd/reconcile

FROM scratch AS runtime
COPY --from=build /etc/ssl/certs/ca-certificates.crt /etc/ssl/certs/ca-certificates.crt
WORKDIR /app
USER 65532:65532

FROM runtime AS api
COPY --from=build /out/api /usr/local/bin/api
CMD ["/usr/local/bin/api"]

FROM runtime AS worker
COPY --from=build /out/worker /usr/local/bin/worker
CMD ["/usr/local/bin/worker"]

FROM runtime AS mockbank
COPY --from=build /out/mockbank /usr/local/bin/mockbank
CMD ["/usr/local/bin/mockbank"]

FROM golang:1.26 AS migrate
WORKDIR /app
ARG TARGETARCH
RUN set -eu; \
    case "$TARGETARCH" in \
      amd64) digest=4a3814fdb7253348d6b3cd60a2e315fba69e1a3a794a31ec75af2ca6019c835c ;; \
      arm64) digest=bccaa28783d8c05b060b2bb191079487b700e5cd51ae76bea2393c3c93153e99 ;; \
      *) echo "Unsupported dbmate architecture: $TARGETARCH" >&2; exit 1 ;; \
    esac; \
    curl -fsSL --retry 3 "https://github.com/amacneil/dbmate/releases/download/v2.28.0/dbmate-linux-$TARGETARCH" -o /usr/local/bin/dbmate; \
    echo "$digest  /usr/local/bin/dbmate" | sha256sum -c -; \
    chmod 0755 /usr/local/bin/dbmate
COPY internal/transfer/migrations/ internal/transfer/migrations/
USER 65532:65532
CMD ["dbmate", "--env", "DATABASE_URL", "--migrations-dir", "internal/transfer/migrations", "--migrations-table", "dbmate_schema_migrations", "--no-dump-schema", "migrate"]

FROM runtime AS all
COPY --from=build /out/ /usr/local/bin/
CMD ["/usr/local/bin/api"]
