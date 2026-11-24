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
EXPOSE 8080
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
RUN --mount=type=bind,source=scripts/install-dbmate.sh,target=/tmp/install-dbmate.sh sh /tmp/install-dbmate.sh "$TARGETARCH"
COPY internal/transfer/migrations/ internal/transfer/migrations/
COPY --chmod=0555 scripts/migrate.sh /usr/local/bin/transfer-migrate
USER 65532:65532
CMD ["/usr/local/bin/transfer-migrate"]

FROM runtime AS all
COPY --from=build /out/ /usr/local/bin/
EXPOSE 8080
CMD ["/usr/local/bin/api"]
