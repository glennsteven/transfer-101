#!/bin/sh
set -eu

if [ "$#" -ne 1 ]; then
    printf '%s\n' 'Usage from repository root: sh scripts/build-container-tests.sh arm64|amd64' >&2
    exit 2
fi
test_arch=$1
case "$test_arch" in
    arm64|amd64) ;;
    *) printf 'Unsupported container architecture: %s\n' "$test_arch" >&2; exit 2 ;;
esac

if [ ! -f go.mod ] || [ ! -f scripts/run-container-tests.sh ]; then
    printf '%s\n' 'Run this command from the repository root.' >&2
    exit 2
fi

mkdir -p bin/integration
dbmate_version=2.28.0
# SHA-256 digests from the upstream v2.28.0 release assets.
case "$test_arch" in
    arm64) dbmate_sha256=bccaa28783d8c05b060b2bb191079487b700e5cd51ae76bea2393c3c93153e99 ;;
    amd64) dbmate_sha256=4a3814fdb7253348d6b3cd60a2e315fba69e1a3a794a31ec75af2ca6019c835c ;;
esac
verify_dbmate() {
    printf '%s  %s\n' "$dbmate_sha256" bin/integration/dbmate | shasum -a 256 -c - >/dev/null 2>&1
}
if ! verify_dbmate; then
    printf 'Downloading dbmate %s for linux/%s\n' "$dbmate_version" "$test_arch"
    curl --fail --location --silent --show-error \
        "https://github.com/amacneil/dbmate/releases/download/v$dbmate_version/dbmate-linux-$test_arch" \
        -o bin/integration/dbmate.download
    mv bin/integration/dbmate.download bin/integration/dbmate
    if ! verify_dbmate; then
        printf '%s\n' 'dbmate SHA-256 mismatch; refusing to use the downloaded binary.' >&2
        rm -f bin/integration/dbmate
        exit 1
    fi
fi
chmod +x bin/integration/dbmate
mkdir -p bin/integration/migrations
rm -f bin/integration/migrations/*.sql
cp internal/transfer/migrations/*.sql bin/integration/migrations/
mkdir -p bin/integration/mockbank-migrations
rm -f bin/integration/mockbank-migrations/*.sql
cp internal/mockbank/migrations/*.sql bin/integration/mockbank-migrations/
for test_name in transfer mockbank health api mockbank-process; do
    case "$test_name" in
        api) test_package=./cmd/api ;;
        mockbank-process) test_package=./cmd/mockbank ;;
        *) test_package="./internal/$test_name" ;;
    esac
    printf 'Building linux/%s test binary: %s\n' "$test_arch" "$test_name"
    GOOS=linux GOARCH="$test_arch" CGO_ENABLED=0 go test -c -tags=integration \
        -o "bin/integration/$test_name.test" "$test_package"
done
GOOS=linux GOARCH="$test_arch" CGO_ENABLED=0 go build -o bin/integration/demo-provider ./cmd/demo-provider
GOOS=linux GOARCH="$test_arch" CGO_ENABLED=0 go build -o bin/integration/recovery-worker ./cmd/worker
GOOS=linux GOARCH="$test_arch" CGO_ENABLED=0 go build -o bin/integration/funds ./cmd/funds
GOOS=linux GOARCH="$test_arch" CGO_ENABLED=0 go build -o bin/integration/reconcile ./cmd/reconcile
GOOS=linux GOARCH="$test_arch" CGO_ENABLED=0 go build -o bin/integration/loadtest ./cmd/loadtest
GOOS=linux GOARCH="$test_arch" CGO_ENABLED=0 go build -o bin/integration/api ./cmd/api
GOOS=linux GOARCH="$test_arch" CGO_ENABLED=0 go build -o bin/integration/mockbank ./cmd/mockbank
cp scripts/run-container-tests.sh bin/integration/run-container-tests.sh
