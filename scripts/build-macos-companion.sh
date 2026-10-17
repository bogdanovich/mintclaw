#!/usr/bin/env bash
# Package the real companion as an explicitly launched GUI application. This
# neither installs/launches it nor alters a service, config, TCC or permissions.
set -euo pipefail

usage() {
  echo 'Usage: build-macos-companion.sh --binary /absolute/mintclaw-node --config /absolute/config.json --output /absolute/new.app (--sign-identity identity | --development-ad-hoc)'
  echo 'Launch the built app through Finder or open, not its embedded executable via SSH.'
  echo 'Ad-hoc development signatures may require permission reapproval after rebuilding.'
}

node_binary=''
node_config=''
application_path=''
sign_identity=''
while [[ $# -gt 0 ]]; do
  case "$1" in
    --binary|--config|--output|--sign-identity)
      [[ $# -ge 2 && -n "$2" ]] || { usage; exit 2; }
      case "$1" in
        --binary) node_binary=$2 ;;
        --config) node_config=$2 ;;
        --output) application_path=$2 ;;
        --sign-identity)
          [[ -z "$sign_identity" && "$2" != '-' ]] || { usage; exit 2; }
          sign_identity=$2 ;;
      esac
      shift 2 ;;
    --development-ad-hoc)
      [[ -z "$sign_identity" ]] || { usage; exit 2; }
      sign_identity='-'; shift ;;
    --help|-h) usage; exit 0 ;;
    *) usage; exit 2 ;;
  esac
done
[[ $(uname -s) == Darwin ]] || { echo 'macOS packaging requires macOS.' >&2; exit 2; }
[[ -n "$sign_identity" && "$node_binary" == /* && "$node_config" == /* && "$application_path" == /*.app ]] || {
  usage; exit 2
}
[[ -f "$node_binary" && ! -L "$node_binary" && -x "$node_binary" &&
   -f "$node_config" && ! -L "$node_config" && ! -e "$application_path" && ! -L "$application_path" ]] || {
  echo 'Use regular input files and a new output path; existing applications are never replaced.' >&2
  exit 2
}
/usr/bin/lipo "$node_binary" -verify_arch "$(uname -m)"
repository_root=$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)
# mkdir reserves this exact new output, so a second build cannot merge into it.
mkdir -m 700 "$application_path"
mkdir "$application_path/Contents" "$application_path/Contents/MacOS"
cp "$node_binary" "$application_path/Contents/MacOS/mintclaw-node"
cp "$repository_root/scripts/internal/macos-companion/Info.plist" "$application_path/Contents/Info.plist"
/usr/bin/plutil -insert MintClawNodeConfigPath -string "$node_config" "$application_path/Contents/Info.plist"
/usr/bin/codesign --force --sign "$sign_identity" --options runtime --timestamp=none "$application_path"
/usr/bin/codesign --verify --strict "$application_path"
echo "Built $application_path; no service or permission changes were made."
if [[ "$sign_identity" == '-' ]]; then
  echo 'Development-only ad-hoc identity: do not assume TCC grants survive a rebuild.'
fi
