#!/usr/bin/env bash
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
ROOT_DIR="$(cd "${SCRIPT_DIR}/.." && pwd)"

TARGET_REPO="${TARGET_REPO:-LightToUs/ppanel-node}"
TARGET_BRANCH="${TARGET_BRANCH:-main}"
TARGET_REMOTE="${TARGET_REMOTE:-lighttous}"
ALLOW_DIRTY="${ALLOW_DIRTY:-false}"
WAIT_FOR_ASSETS="${WAIT_FOR_ASSETS:-true}"
RELEASE_TIMEOUT_SEC="${RELEASE_TIMEOUT_SEC:-1800}"
POLL_INTERVAL_SEC="${POLL_INTERVAL_SEC:-15}"
EXPECTED_ASSETS="${EXPECTED_ASSETS:-ppanel-node-linux-64.zip,ppanel-node-linux-arm64-v8a.zip}"

require_cmd() {
  if ! command -v "$1" >/dev/null 2>&1; then
    echo "Error: command '$1' is required." >&2
    exit 1
  fi
}

usage() {
  cat <<EOF
Usage:
  $0 <version-tag>

Example:
  $0 v1.0.2

Environment variables:
  TARGET_REPO         GitHub repo slug, default: ${TARGET_REPO}
  TARGET_BRANCH       Branch that raw scripts should serve from, default: ${TARGET_BRANCH}
  TARGET_REMOTE       Local git remote name to use/create, default: ${TARGET_REMOTE}
  ALLOW_DIRTY         Allow publishing with uncommitted changes, default: ${ALLOW_DIRTY}
  WAIT_FOR_ASSETS     Wait for GitHub Actions release assets, default: ${WAIT_FOR_ASSETS}
  RELEASE_TIMEOUT_SEC Wait timeout in seconds, default: ${RELEASE_TIMEOUT_SEC}
  POLL_INTERVAL_SEC   Poll interval in seconds, default: ${POLL_INTERVAL_SEC}
  EXPECTED_ASSETS     Comma-separated asset names to wait for
EOF
}

if [[ $# -ne 1 ]]; then
  usage >&2
  exit 1
fi

VERSION_TAG="$1"

require_cmd git
require_cmd gh
require_cmd curl

cd "${ROOT_DIR}"

if [[ "${ALLOW_DIRTY}" != "true" ]] && [[ -n "$(git status --porcelain)" ]]; then
  echo "Error: working tree is not clean. Commit or stash changes first, or set ALLOW_DIRTY=true." >&2
  exit 1
fi

if ! git rev-parse --verify HEAD >/dev/null 2>&1; then
  echo "Error: current branch has no commits yet. Commit your local source first, then rerun the publish script." >&2
  exit 1
fi

if ! gh auth status >/dev/null 2>&1; then
  echo "Error: gh is not authenticated. Run 'gh auth login' first." >&2
  exit 1
fi

REMOTE_URL="https://github.com/${TARGET_REPO}.git"
if git remote get-url "${TARGET_REMOTE}" >/dev/null 2>&1; then
  git remote set-url "${TARGET_REMOTE}" "${REMOTE_URL}"
else
  git remote add "${TARGET_REMOTE}" "${REMOTE_URL}"
fi

git fetch "${TARGET_REMOTE}" --tags >/dev/null 2>&1 || true

if git rev-parse -q --verify "refs/tags/${VERSION_TAG}" >/dev/null 2>&1; then
  echo "Error: local tag ${VERSION_TAG} already exists." >&2
  exit 1
fi

if git ls-remote --exit-code --tags "${TARGET_REMOTE}" "refs/tags/${VERSION_TAG}" >/dev/null 2>&1; then
  echo "Error: remote tag ${VERSION_TAG} already exists in ${TARGET_REPO}." >&2
  exit 1
fi

if gh release view "${VERSION_TAG}" --repo "${TARGET_REPO}" >/dev/null 2>&1; then
  echo "Error: release ${VERSION_TAG} already exists in ${TARGET_REPO}." >&2
  exit 1
fi

CURRENT_SHA="$(git rev-parse --short HEAD)"
RELEASE_NOTES=$(cat <<EOF
Automated release ${VERSION_TAG}

- Source commit: ${CURRENT_SHA}
- Source branch: $(git rev-parse --abbrev-ref HEAD)
- Published to: ${TARGET_REPO}
EOF
)

echo "Publishing ${VERSION_TAG} to ${TARGET_REPO}"
echo "  branch: ${TARGET_BRANCH}"
echo "  commit: ${CURRENT_SHA}"
echo "  remote: ${TARGET_REMOTE} -> ${REMOTE_URL}"

git push "${TARGET_REMOTE}" HEAD:"refs/heads/${TARGET_BRANCH}"
git tag -a "${VERSION_TAG}" -m "Release ${VERSION_TAG}"
git push "${TARGET_REMOTE}" "refs/tags/${VERSION_TAG}"

gh release create "${VERSION_TAG}" \
  --repo "${TARGET_REPO}" \
  --title "${VERSION_TAG}" \
  --notes "${RELEASE_NOTES}" \
  --verify-tag

RAW_BASE="https://raw.githubusercontent.com/${TARGET_REPO}/${TARGET_BRANCH}"

echo "Verifying raw scripts..."
curl -fsSL "${RAW_BASE}/scripts/install.sh" >/dev/null
curl -fsSL "${RAW_BASE}/scripts/ppnode.sh" >/dev/null

if [[ "${WAIT_FOR_ASSETS}" == "true" ]]; then
  IFS=',' read -r -a expected_assets <<< "${EXPECTED_ASSETS}"
  deadline=$(( $(date +%s) + RELEASE_TIMEOUT_SEC ))
  echo "Waiting for release assets from GitHub Actions..."
  while true; do
    asset_names="$(gh release view "${VERSION_TAG}" --repo "${TARGET_REPO}" --json assets --jq '.assets[].name' || true)"
    all_present=true
    for asset in "${expected_assets[@]}"; do
      if ! grep -Fxq "${asset}" <<< "${asset_names}"; then
        all_present=false
        break
      fi
    done
    if [[ "${all_present}" == "true" ]]; then
      echo "Required assets are ready."
      break
    fi
    if (( $(date +%s) >= deadline )); then
      echo "Error: timed out waiting for release assets: ${EXPECTED_ASSETS}" >&2
      exit 1
    fi
    sleep "${POLL_INTERVAL_SEC}"
  done
fi

cat <<EOF

Release published successfully.

Repository:
  https://github.com/${TARGET_REPO}

Release:
  https://github.com/${TARGET_REPO}/releases/tag/${VERSION_TAG}

Compatible install command:
  wget -N ${RAW_BASE}/scripts/install.sh && bash install.sh

Compatible update command on node host:
  PPNODE_GITHUB_REPO=${TARGET_REPO} ppnode update ${VERSION_TAG}

One-time migration for existing old ppnode.sh:
  wget -O /usr/bin/ppnode -N --no-check-certificate ${RAW_BASE}/scripts/ppnode.sh && chmod +x /usr/bin/ppnode
EOF
