#!/usr/bin/env bash
set -euo pipefail

BINARY_NAME="${BINARY_NAME:-hubfly-builder}"
DEPLOY_SERVERS="${DEPLOY_SERVERS:-root@100.66.212.61 root@100.66.229.114}"
REMOTE_BIN_PATH="${REMOTE_BIN_PATH:-/usr/local/bin/${BINARY_NAME}}"
REMOTE_TMP_DIR="${REMOTE_TMP_DIR:-/tmp/hubfly-builder-deploy}"
REMOTE_SERVICE_NAME="${REMOTE_SERVICE_NAME:-hubfly-builder.service}"
REMOTE_USER="${REMOTE_USER:-hubfly-builder}"
REMOTE_GROUP="${REMOTE_GROUP:-hubfly-builder}"
REMOTE_CONFIG_DIR="${REMOTE_CONFIG_DIR:-/etc/hubfly-builder}"
REMOTE_STATE_DIR="${REMOTE_STATE_DIR:-/var/lib/hubfly-builder}"
REMOTE_LOG_DIR="${REMOTE_LOG_DIR:-/var/log/hubfly-builder}"
UPDATE_LOCKFILE="${UPDATE_LOCKFILE:-/run/hubfly-builder-update.lock}"

SSH_OPTS=(
  -o BatchMode=yes
  -o ConnectTimeout="${SSH_CONNECT_TIMEOUT:-20}"
  -o ServerAliveInterval=15
  -o ServerAliveCountMax=3
  -o StrictHostKeyChecking="${SSH_STRICT_HOST_KEY_CHECKING:-accept-new}"
)

require_cmd() {
  if ! command -v "$1" >/dev/null 2>&1; then
    echo "missing required command: $1" >&2
    exit 1
  fi
}

for cmd in ssh scp go sed; do
  require_cmd "$cmd"
done
if [[ "$(uname -s)" == "Linux" ]]; then
  require_cmd gcc
fi

build_for_arch() {
  local arch="$1"
  local out="dist/deploy/${BINARY_NAME}-linux-${arch}"
  if [[ ! -x "${out}" ]]; then
    mkdir -p dist/deploy
    echo "Building ${BINARY_NAME} for linux/${arch}..." >&2
    CGO_ENABLED=1 GOOS=linux GOARCH="${arch}" go build -o "${out}" ./cmd/hubfly-builder/main.go
    chmod +x "${out}"
  fi
  printf '%s' "${out}"
}

deploy_one() {
  local server="$1"
  local remote_arch goarch local_bin remote_tmp_path

  echo "==> Deploying ${BINARY_NAME} to ${server}"
  remote_arch="$(ssh "${SSH_OPTS[@]}" "${server}" 'uname -m')"
  case "${remote_arch}" in
    x86_64|amd64) goarch="amd64" ;;
    aarch64|arm64) goarch="arm64" ;;
    *) echo "unsupported remote architecture on ${server}: ${remote_arch}" >&2; return 1 ;;
  esac
  local_bin="$(build_for_arch "${goarch}")"
  remote_tmp_path="${REMOTE_TMP_DIR}/${BINARY_NAME}-linux-${goarch}"

  ssh "${SSH_OPTS[@]}" "${server}" "mkdir -p '${REMOTE_TMP_DIR}'"
  scp "${SSH_OPTS[@]}" "${local_bin}" "${server}:${remote_tmp_path}"
  ssh "${SSH_OPTS[@]}" "${server}" \
    "BINARY_NAME='${BINARY_NAME}' \
REMOTE_BIN_PATH='${REMOTE_BIN_PATH}' \
REMOTE_TMP_PATH='${remote_tmp_path}' \
REMOTE_SERVICE_NAME='${REMOTE_SERVICE_NAME}' \
REMOTE_USER='${REMOTE_USER}' \
REMOTE_GROUP='${REMOTE_GROUP}' \
REMOTE_CONFIG_DIR='${REMOTE_CONFIG_DIR}' \
REMOTE_STATE_DIR='${REMOTE_STATE_DIR}' \
REMOTE_LOG_DIR='${REMOTE_LOG_DIR}' \
UPDATE_LOCKFILE='${UPDATE_LOCKFILE}' \
bash -s" <<'REMOTE'
set -euo pipefail

if ! getent group "${REMOTE_GROUP}" >/dev/null 2>&1; then
  groupadd --system "${REMOTE_GROUP}"
fi
if ! id -u "${REMOTE_USER}" >/dev/null 2>&1; then
  useradd --system --gid "${REMOTE_GROUP}" --home-dir "${REMOTE_STATE_DIR}" --shell /usr/sbin/nologin "${REMOTE_USER}"
fi

install -d -o "${REMOTE_USER}" -g "${REMOTE_GROUP}" -m 0750 "${REMOTE_CONFIG_DIR}" "${REMOTE_STATE_DIR}" "${REMOTE_LOG_DIR}"
chown -R "${REMOTE_USER}:${REMOTE_GROUP}" "${REMOTE_CONFIG_DIR}" "${REMOTE_STATE_DIR}" "${REMOTE_LOG_DIR}"
chmod 0750 "${REMOTE_CONFIG_DIR}" "${REMOTE_STATE_DIR}" "${REMOTE_LOG_DIR}"
install -d -m 0755 /usr/local/bin /etc/systemd/system /etc/sudoers.d

while [[ -f "${UPDATE_LOCKFILE}" ]]; do
  echo "Active build detected; waiting 10s..."
  sleep 10
done

backup_path=""
if [[ -f "${REMOTE_BIN_PATH}" ]]; then
  backup_path="${REMOTE_BIN_PATH}.bak.$(date -u +%Y%m%dT%H%M%SZ)"
  cp "${REMOTE_BIN_PATH}" "${backup_path}"
fi

install -m 0755 "${REMOTE_TMP_PATH}" "${REMOTE_BIN_PATH}"
rm -f "${REMOTE_TMP_PATH}"

cat >/etc/systemd/system/${REMOTE_SERVICE_NAME} <<EOF
[Unit]
Description=Hubfly Builder
After=network-online.target
Wants=network-online.target

[Service]
Type=simple
User=${REMOTE_USER}
Group=${REMOTE_GROUP}
WorkingDirectory=${REMOTE_STATE_DIR}
Environment=HUBFLY_BUILDER_CONFIG=${REMOTE_CONFIG_DIR}/config.json
ExecStartPre=+/usr/bin/install -d -o ${REMOTE_USER} -g ${REMOTE_GROUP} -m 0750 ${REMOTE_CONFIG_DIR}
ExecStartPre=+/usr/bin/install -d -o ${REMOTE_USER} -g ${REMOTE_GROUP} -m 0750 ${REMOTE_STATE_DIR} ${REMOTE_LOG_DIR}
ExecStart=${REMOTE_BIN_PATH}
Restart=always
RestartSec=3

[Install]
WantedBy=multi-user.target
EOF

cat >/etc/sudoers.d/hubfly-builder <<'EOF'
hubfly-builder ALL=(root) NOPASSWD: /usr/local/bin/hubcell
EOF
chmod 0440 /etc/sudoers.d/hubfly-builder
visudo -cf /etc/sudoers.d/hubfly-builder >/dev/null

systemctl daemon-reload
systemctl enable "${REMOTE_SERVICE_NAME}"
if ! systemctl restart "${REMOTE_SERVICE_NAME}"; then
  if [[ -n "${backup_path}" && -f "${backup_path}" ]]; then
    install -m 0755 "${backup_path}" "${REMOTE_BIN_PATH}"
    systemctl restart "${REMOTE_SERVICE_NAME}" || true
  fi
  exit 1
fi
systemctl is-active "${REMOTE_SERVICE_NAME}" >/dev/null
systemctl --no-pager --full status "${REMOTE_SERVICE_NAME}" | sed -n '1,20p'
REMOTE
}

for server in ${DEPLOY_SERVERS}; do
  deploy_one "${server}"
done

echo "Deploy complete."
