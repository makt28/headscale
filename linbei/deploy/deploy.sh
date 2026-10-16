#!/usr/bin/env bash
# 在 Mac 上运行：编译、上传、在服务器上执行 install.sh。
#   bash linbei/deploy/deploy.sh <user@host> <域名> check                          只检查服务器，不改东西
#   bash linbei/deploy/deploy.sh <user@host> <域名> [install] [--admin-key-file F]   安装或升级
#   bash linbei/deploy/deploy.sh <user@host> <域名> install --restore F             在新机器上从备份重建
#   bash linbei/deploy/deploy.sh <user@host> <域名> backup                          备份到当前目录
# 管理密钥首次安装必须给（和 sspapi data/tailnet.json 的 admin_key 相同），之后升级可省略。
# SSH 密码（如有）由你在终端里输；同一个连接复用 10 分钟，check 之后紧接着 install 不用再输。
set -euo pipefail

usage() {
    sed -n '2,8p' "$0" >&2
    exit 1
}

HOST="${1:-}"
DOMAIN="${2:-}"
[[ -n "${HOST}" && -n "${DOMAIN}" ]] || usage
shift 2
MODE=install
ADMIN_KEY_FILE=""
RESTORE=""
while (($# > 0)); do
    case "$1" in
        check | install | backup) MODE="$1"; shift ;;
        --admin-key-file) ADMIN_KEY_FILE="${2:-}"; shift 2 || usage ;;
        --restore) RESTORE="${2:-}"; shift 2 || usage ;;
        *) usage ;;
    esac
done
[[ -z "${ADMIN_KEY_FILE}" || -s "${ADMIN_KEY_FILE}" ]] || { echo "读不到管理密钥文件：${ADMIN_KEY_FILE}" >&2; exit 1; }
[[ -z "${RESTORE}" || -s "${RESTORE}" ]] || { echo "读不到备份包：${RESTORE}" >&2; exit 1; }

REPO="$(cd "$(dirname "$0")/../.." && pwd)"
STAGE="$(mktemp -d)"
trap 'rm -rf "${STAGE}"' EXIT
chmod 700 "${STAGE}"

SSH_OPTS=(-o ControlMaster=auto -o "ControlPath=${HOME}/.ssh/cm-linbei-%C" -o ControlPersist=10m)
SUDO="sudo"
[[ "${HOST%%@*}" == root ]] && SUDO=""
REMOTE=/tmp/linbei-headscale

if [[ "${MODE}" != backup ]]; then
    echo "== 编译 headscale（linux/amd64）"
    (cd "${REPO}" && CGO_ENABLED=0 GOOS=linux GOARCH=amd64 \
        go build -trimpath -ldflags "-s -w" -o "${STAGE}/headscale" ./cmd/headscale)
    cp "${REPO}/packaging/systemd/headscale.service" "${STAGE}/headscale.service"
    cp "${REPO}/linbei/derp-map.yaml" "${STAGE}/linbei-derp-map.yaml"
fi
cp "${REPO}/linbei/deploy/install.sh" "${STAGE}/install.sh"
if [[ -n "${ADMIN_KEY_FILE}" ]]; then
    (umask 077 && cp "${ADMIN_KEY_FILE}" "${STAGE}/admin_key")
fi
if [[ -n "${RESTORE}" ]]; then
    cp "${RESTORE}" "${STAGE}/restore.tgz"
fi

echo "== 上传到 ${HOST}:${REMOTE}"
ssh "${SSH_OPTS[@]}" "${HOST}" "rm -rf ${REMOTE} && mkdir -m 700 -p ${REMOTE}"
scp -q "${SSH_OPTS[@]}" "${STAGE}"/* "${HOST}:${REMOTE}/"

echo "== 在服务器上执行 install.sh ${MODE}"
# 结束后删掉上传的东西（里面有管理密钥）；backup 模式先把备份包拉回来再删。
if [[ "${MODE}" == backup ]]; then
    ssh -t "${SSH_OPTS[@]}" "${HOST}" "${SUDO} bash ${REMOTE}/install.sh backup ${DOMAIN}"
    OUT="headscale-backup-$(date +%Y%m%d-%H%M%S).tgz"
    (umask 077 && scp -q "${SSH_OPTS[@]}" "${HOST}:${REMOTE}/backup.tgz" "${OUT}")
    ssh "${SSH_OPTS[@]}" "${HOST}" "${SUDO} rm -rf ${REMOTE}"
    echo "备份已保存到 $(pwd)/${OUT}（含 noise 私钥，妥善保管）"
else
    status=0
    ssh -t "${SSH_OPTS[@]}" "${HOST}" "${SUDO} bash ${REMOTE}/install.sh ${MODE} ${DOMAIN}" || status=$?
    ssh "${SSH_OPTS[@]}" "${HOST}" "${SUDO} rm -rf ${REMOTE}" || true
    exit "${status}"
fi
