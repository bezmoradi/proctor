#!/bin/sh
set -eu
copy=$1
docker compose -p proctor-sample -f "$copy/examples/sample/compose.yaml" down -v --rmi local
