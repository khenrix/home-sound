#!/usr/bin/env bash
# SPDX-License-Identifier: GPL-3.0-or-later
set -euo pipefail
umask 022
here=$(cd -- "$(dirname -- "$0")" && pwd)
source_dir=${1:?usage: build.sh NEW_SOURCE_DIRECTORY OUTPUT_BINARY}
binary=${2:?output binary required}
go_binary=${GO_BINARY:-go}
pin=90a4fdaf0314269e3fa25698a5f4270c026d2c03
cd "$here"
sha256sum -c SHA256SUMS
if test -e "$source_dir"; then
  printf '%s\n' 'source destination must not exist' >&2
  exit 1
fi
git clone --no-checkout https://github.com/devgianlu/go-librespot.git "$source_dir"
git -C "$source_dir" checkout --detach "$pin"
git -C "$source_dir" apply --check "$here/framed.patch"
git -C "$source_dir" apply "$here/framed.patch"
git -C "$source_dir" apply --check "$here/native-decoder.patch"
git -C "$source_dir" apply "$here/native-decoder.patch"
mkdir -p "$source_dir/vorbis/testdata"
cp "$here/native-decoder-tone.ogg" "$source_dir/vorbis/testdata/close-tone.ogg"
cd "$source_dir"
"$go_binary" version
"$go_binary" test -race -tags test_unit -count=1 ./...
"$go_binary" vet -tags test_unit ./...
"$go_binary" build -trimpath -o "$binary" ./cmd/daemon
sha256sum "$binary"
