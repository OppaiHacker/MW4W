#!/bin/sh
# Builds dist/MW4W-Setup.exe from the repo: ./installer/build.sh [version]
set -e
cd "$(dirname "$0")"
version="${1:-$(git describe --tags --always 2>/dev/null || echo dev)}"
rm -rf payload && mkdir -p payload ../dist
cp ../install.ps1 ../uninstall.ps1 ../LICENSE payload/
cp -r ../wallpapers payload/
# the pack as committed: no cache/, colors.css or settings.json from a dev checkout
git -C .. archive HEAD pack | tar -x -C payload
GOOS=windows GOARCH=amd64 CGO_ENABLED=0 go build -trimpath -ldflags "-s -w -X main.version=$version" -o ../dist/MW4W-Setup.exe .
rm -rf payload
echo "dist/MW4W-Setup.exe ($version)"
