#!/usr/bin/env bash
# SPDX-License-Identifier: GPL-3.0-or-later
set -euo pipefail
umask 022
here=$(cd -- "$(dirname -- "$0")" && pwd)
source_dir=${1:?patched corresponding source required}
binary=${2:?absolute fixture binary path required}
go_binary=${GO_BINARY:-go}
test "$(git -C "$source_dir" rev-parse HEAD)" = 90a4fdaf0314269e3fa25698a5f4270c026d2c03
git -C "$source_dir" apply --reverse --check "$here/framed.patch"
overlay="${binary}.overlay.json"
python3 - "$source_dir" "$here" "$overlay" <<'PY'
import json,sys
source,here,overlay=sys.argv[1:]
with open(overlay,'w') as f:
    json.dump({'Replace':{source+'/daemon/core_consumer_test.go':here+'/core-consumer-fixture.go.txt'}},f)
PY
cd "$source_dir"
"$go_binary" test -race -tags test_unit -overlay "$overlay" -c -o "$binary" ./daemon
sha256sum "$binary" "$here/core-consumer-fixture.go.txt"
