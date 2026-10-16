#!/usr/bin/env bash
# 在服务器上以 root 运行：安装 / 升级林北 headscale。
#   install.sh check   <域名>   只检查，不改任何东西
#   install.sh install <域名>   安装或升级（可重复执行；已有的 token、数据库、API key 不动）
# 同目录要有 headscale（Linux 二进制）、headscale.service、linbei-derp-map.yaml。
set -euo pipefail

MODE="${1:-}"
DOMAIN="${2:-}"
HERE="$(cd "$(dirname "$0")" && pwd)"
ETC=/etc/headscale
VAR=/var/lib/headscale
TOKEN_FILE="${ETC}/derp_node_token"

die() { echo "错误：$*" >&2; exit 1; }
ok() { echo "  [好] $*"; }
bad() { echo "  [不行] $*"; FAILED=1; }
warn() { echo "  [注意] $*"; }

[[ "${MODE}" == check || "${MODE}" == install ]] || die "用法：install.sh check|install <域名>"
[[ "${DOMAIN}" =~ ^[A-Za-z0-9.-]+\.[A-Za-z]+$ ]] || die "域名不对：${DOMAIN}"
[[ "$(id -u)" == 0 ]] || die "要用 root 运行"

do_check() {
    FAILED=0
    echo "== 检查 ${DOMAIN}"
    command -v systemctl >/dev/null && ok "有 systemd" || bad "没有 systemd"
    [[ "$(uname -m)" == x86_64 ]] && ok "架构 x86_64" || bad "架构是 $(uname -m)，二进制只编了 x86_64"
    command -v curl >/dev/null && ok "有 curl" || bad "没有 curl（apt install curl）"

    local busy
    for port in 80 443; do
        busy="$(ss -Hltnp "sport = :${port}" 2>/dev/null | grep -v '"headscale"' || true)"
        [[ -z "${busy}" ]] && ok "端口 ${port} 空着" || bad "端口 ${port} 被占用：${busy}"
    done

    local resolved public
    resolved="$(getent ahostsv4 "${DOMAIN}" | awk 'NR==1{print $1}')"
    public="$(curl -4 -fsS --max-time 8 https://api.ipify.org 2>/dev/null || true)"
    if [[ -z "${resolved}" ]]; then
        bad "${DOMAIN} 解析不出 IPv4"
    elif [[ -n "${public}" && "${resolved}" != "${public}" ]]; then
        bad "${DOMAIN} 解析到 ${resolved}，但本机公网 IP 是 ${public}（没指过来，或开了 Cloudflare 代理）"
    else
        ok "${DOMAIN} 解析到 ${resolved}${public:+（= 本机公网 IP）}"
    fi

    if command -v ufw >/dev/null && ufw status 2>/dev/null | grep -q "Status: active"; then
        warn "ufw 开着，安装时会放行 80/443"
    fi
    [[ -f "${ETC}/config.yaml" ]] && warn "已经装过，install 会升级二进制与配置（数据库、token、API key 保留）"
    echo "  云厂商安全组要放行 TCP 80 和 443，这一项脚本查不到"
    return "${FAILED}"
}

do_install() {
    do_check || die "检查没过，什么都没改"
    echo "== 安装"
    id headscale >/dev/null 2>&1 ||
        useradd --system --shell /usr/sbin/nologin --home-dir "${VAR}" headscale
    mkdir -p "${ETC}" "${VAR}"
    chown headscale:headscale "${VAR}" && chmod 750 "${VAR}"

    systemctl stop headscale 2>/dev/null || true
    install -m 755 "${HERE}/headscale" /usr/bin/headscale
    install -m 644 "${HERE}/headscale.service" /etc/systemd/system/headscale.service
    install -m 644 "${HERE}/linbei-derp-map.yaml" "${ETC}/linbei-derp-map.yaml"

    if [[ ! -s "${TOKEN_FILE}" ]]; then
        (umask 077 && head -c 32 /dev/urandom | od -An -tx1 | tr -d ' \n' >"${TOKEN_FILE}")
        echo "  已生成节点 token"
    fi
    chown root:headscale "${TOKEN_FILE}" && chmod 640 "${TOKEN_FILE}"

    cat >"${ETC}/config.yaml" <<EOF
# 由 linbei/deploy/install.sh 生成，重新安装会覆盖。
server_url: https://${DOMAIN}
listen_addr: 0.0.0.0:443
metrics_listen_addr: 127.0.0.1:9090
grpc_listen_addr: 127.0.0.1:50443
trusted_proxies: []

tls_letsencrypt_hostname: ${DOMAIN}
tls_letsencrypt_cache_dir: ${VAR}/cache
tls_letsencrypt_challenge_type: HTTP-01
tls_letsencrypt_listen: ":http"

noise:
  private_key_path: ${VAR}/noise_private.key
prefixes:
  v4: 100.64.0.0/10
  v6: fd7a:115c:a1e0::/48
  allocation: sequential

derp:
  server:
    enabled: false
  urls: []
  paths:
    - ${ETC}/linbei-derp-map.yaml
  auto_update_enabled: false
  update_frequency: 3h

disable_check_updates: true

database:
  type: sqlite
  sqlite:
    path: ${VAR}/db.sqlite
    write_ahead_log: true

log:
  level: info

policy:
  mode: file
  path: ""

dns:
  magic_dns: true
  base_domain: ts.linbei.internal
  override_local_dns: false

unix_socket: /var/run/headscale/headscale.sock
unix_socket_permission: "0770"

linbei:
  derp_mesh:
    node_token_path: ${TOKEN_FILE}
    expiry: 24h
EOF
    chown root:headscale "${ETC}/config.yaml" && chmod 640 "${ETC}/config.yaml"

    if command -v ufw >/dev/null && ufw status 2>/dev/null | grep -q "Status: active"; then
        ufw allow 80/tcp >/dev/null && ufw allow 443/tcp >/dev/null && echo "  ufw 已放行 80/443"
    fi

    systemctl daemon-reload
    systemctl enable --now headscale
    echo "  等 headscale 起来并申请证书（最多 90 秒）…"
    local i
    for i in $(seq 1 45); do
        if curl -fsS --max-time 3 --resolve "${DOMAIN}:443:127.0.0.1" "https://${DOMAIN}/health" >/dev/null 2>&1; then
            break
        fi
        sleep 2
    done
    if ! curl -fsS --max-time 3 --resolve "${DOMAIN}:443:127.0.0.1" "https://${DOMAIN}/health" >/dev/null 2>&1; then
        echo "  headscale 没起来或证书没申请到，最近的日志："
        journalctl -u headscale -n 30 --no-pager || true
        die "安装没完成"
    fi
    ok "https://${DOMAIN}/health 正常，证书有效"
    journalctl -u headscale --no-pager | grep -q "linbei DERP mesh registry enabled" &&
        ok "DERP 注册表已开启" || warn "日志里没看到 DERP 注册表开启"

    local apikey_marker="${VAR}/.linbei_sspapi_key_created"
    echo
    echo "================ 需要你记下的 ================"
    echo "节点 token（每台 SingR 执行 singr derp enable 时用）："
    echo "  $(cat "${TOKEN_FILE}")"
    if [[ ! -f "${apikey_marker}" ]]; then
        local apikey
        apikey="$(headscale apikeys create -e 3650d 2>/dev/null | tail -1 || true)"
        if [[ "${apikey}" == hskey-api-* ]]; then
            echo "sspapi 用的 API key（只显示这一次，填进 sspapi 的 data/tailnet.json）："
            echo "  ${apikey}"
            touch "${apikey_marker}"
        else
            echo "没能生成 API key，稍后手动执行：headscale apikeys create -e 3650d"
        fi
    else
        echo "sspapi 用的 API key：之前已经生成过，不再重复生成（丢了就 headscale apikeys create -e 3650d 再建一把）"
    fi
    echo "要备份：${VAR}/（数据库、noise 私钥、DERP mesh 状态）和 ${TOKEN_FILE}"
    echo "=============================================="
}

"do_${MODE}"
