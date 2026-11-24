#!/bin/sh
set -eu

case "$1" in
    amd64) digest=4a3814fdb7253348d6b3cd60a2e315fba69e1a3a794a31ec75af2ca6019c835c ;;
    arm64) digest=bccaa28783d8c05b060b2bb191079487b700e5cd51ae76bea2393c3c93153e99 ;;
    *) echo "Unsupported dbmate architecture: $1" >&2; exit 1 ;;
esac
curl -fsSL --retry 3 "https://github.com/amacneil/dbmate/releases/download/v2.28.0/dbmate-linux-$1" -o /usr/local/bin/dbmate
echo "$digest  /usr/local/bin/dbmate" | sha256sum -c -
chmod 0755 /usr/local/bin/dbmate
