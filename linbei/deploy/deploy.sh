#!/usr/bin/env bash
# 在 Mac 上运行：编译、上传、在服务器上执行 install.sh。
#   bash linbei/deploy/deploy.sh <user@host> <域名> check     只检查服务器，不改东西
#   bash linbei/deploy/deploy.sh <user@host> <域名>           安装或升级
# SSH 密码（如有）由你在终端里输；同一个连接复用 10 分钟，check 之后紧接着 install 不用再输。
set -euo pipefail

HOST="${1:-}"
DOMAIN="${2:-}"
MODE="${3:-install}"
[[ -n "${HOST}" && -n "${DOMAIN}" ]] || { echo "用法：deploy.sh <user@host> <域名> [check]" >&2; exit 1; }
[[ "${MODE}" == check || "${MODE}" == install ]] || { echo "第三个参数只能是 check" >&2; exit 1; }

REPO="$(cd "$(dirname "$0")/../.." && pwd)"
STAGE="$(mktemp -d)"
trap 'rm -rf "${STAGE}"' EXIT

echo "== 编译 headscale（linux/amd64）"
(cd "${REPO}" && CGO_ENABLED=0 GOOS=linux GOARCH=amd64 \
    go build -trimpath -ldflags "-s -w" -o "${STAGE}/headscale" ./cmd/headscale)
cp "${REPO}/linbei/deploy/install.sh" "${STAGE}/install.sh"
cp "${REPO}/packaging/systemd/headscale.service" "${STAGE}/headscale.service"
cp "${REPO}/linbei/derp-map.yaml" "${STAGE}/linbei-derp-map.yaml"

SSH_OPTS=(-o ControlMaster=auto -o "ControlPath=${HOME}/.ssh/cm-linbei-%C" -o ControlPersist=10m)
SUDO="sudo"
[[ "${HOST%%@*}" == root ]] && SUDO=""
REMOTE=/tmp/linbei-headscale

echo "== 上传到 ${HOST}:${REMOTE}"
ssh "${SSH_OPTS[@]}" "${HOST}" "rm -rf ${REMOTE} && mkdir -p ${REMOTE}"
scp -q "${SSH_OPTS[@]}" "${STAGE}"/* "${HOST}:${REMOTE}/"

echo "== 在服务器上执行 install.sh ${MODE}"
ssh -t "${SSH_OPTS[@]}" "${HOST}" "${SUDO} bash ${REMOTE}/install.sh ${MODE} ${DOMAIN}"
