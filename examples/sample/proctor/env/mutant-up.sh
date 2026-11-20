#!/bin/sh
set -eu
copy=$1
mkdir -p /tmp/proctor-sample-mutant
cp examples/sample/proctor/environments/local.yaml /tmp/proctor-sample-mutant/binding.yaml
docker compose -p proctor-sample -f "$copy/examples/sample/compose.yaml" down -v
docker compose -p proctor-sample -f "$copy/examples/sample/compose.yaml" up -d --build --wait
