#!/bin/sh

set -eu

if [ "$#" -ne 4 ]; then
  echo "expected exactly four node release archives" >&2
  exit 1
fi

for archive in "$@"; do
  if [ ! -f "$archive" ]; then
    echo "missing node release archive: $archive" >&2
    exit 1
  fi

  entries=$(tar -tzf "$archive")
  if [ "$entries" != "mintclaw-node" ]; then
    echo "node release archive must contain only mintclaw-node: $archive" >&2
    exit 1
  fi
done
