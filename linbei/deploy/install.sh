#!/usr/bin/env bash
# 在服务器上以 root 运行：安装 / 升级 / 备份林北 headscale。由 deploy.sh 上传并调用。
#   install.sh check   <域名>   只检查，不改任何东西
#   install.sh install <域名>   安装或升级（可重复执行；数据库与 noise 私钥不动）
#   install.sh backup  <域名>   短暂停服，把 /var/lib/headscale 打包成同目录的 backup.tgz
# 同目录要有 headscale（Linux 二进制）、headscale.service、linbei-derp-map.yaml；
# 可选 admin_key（固定管理密钥，首次安装必须有）、restore.tgz（从备份恢复）。
set -euo pipefail

MODE="${1:-}"
DOMAIN="${2:-}"
HERE="$(cd "$(dirname "$0")" && pwd)"
ETC=/etc/headscale
VAR=/var/lib/headscale
ADMIN_KEY="${ETC}/admin_key"

die() { echo "错误：$*" >&2; exit 1; }
ok() { echo "  [好] $*"; }
bad() { echo "  [不行] $*"; FAILED=1; }
warn() { echo "  [注意] $*"; }

[[ "${MODE}" == check || "${MODE}" == install || "${MODE}" == backup ]] || die "用法：install.sh check|install|backup <域名>"
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
    [[ -f "${ETC}/config.yaml" ]] && warn "已经装过，install 会升级二进制与配置（数据库、noise 私钥保留）"
    if [[ -s "${HERE}/admin_key" ]]; then
        ok "带了管理密钥"
    elif [[ -s "${ADMIN_KEY}" ]]; then
        ok "沿用服务器上已有的管理密钥"
    else
        bad "首次安装要用 --admin-key-file 传管理密钥（和 sspapi data/tailnet.json 的 admin_key 相同）"
    fi
    [[ -s "${HERE}/restore.tgz" ]] && warn "带了备份包，install 会先用它覆盖 ${VAR}（现有的另存一份）"
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

    if [[ -s "${HERE}/admin_key" ]]; then
        local len
        len="$(tr -d ' \r\n' <"${HERE}/admin_key" | wc -c | tr -d ' ')"
        ((len >= 32)) || die "管理密钥至少 32 个字符"
        install -m 640 -o root -g headscale "${HERE}/admin_key" "${ADMIN_KEY}"
        echo "  已写入管理密钥"
    fi
    # 旧版 fork 的 DERP 名单已挪到 sspapi，它的文件用不上了。
    rm -f "${ETC}/derp_node_token" "${VAR}/linbei_derp_mesh.json" "${VAR}/.linbei_sspapi_key_created"

    if [[ -s "${HERE}/restore.tgz" ]]; then
        if [[ -n "$(ls -A "${VAR}" 2>/dev/null)" ]]; then
            local keep="${VAR}.before-restore-$(date +%Y%m%d%H%M%S)"
            cp -a "${VAR}" "${keep}"
            echo "  现有数据另存到 ${keep}"
        fi
        find "${VAR}" -mindepth 1 -delete
        tar -xzf "${HERE}/restore.tgz" -C "${VAR}"
        chown -R headscale:headscale "${VAR}"
        echo "  已从备份恢复 ${VAR}"
    fi

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
  admin_key_path: ${ADMIN_KEY}
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
    # 不能 journalctl | grep -q：grep 找到就退出，journalctl 收到 SIGPIPE，pipefail 下整条管道算失败，
    # 日志一长就误报（输出到 /dev/null 的 grep 也会这样提前退出）。先读进变量再匹配。
    local logs
    logs="$(journalctl -u headscale --no-pager 2>/dev/null || true)"
    if [[ "${logs}" == *"linbei fixed admin key enabled"* ]]; then
        ok "固定管理密钥已生效"
    else
        warn "日志里没看到固定管理密钥生效"
    fi
    echo
    echo "装好了。sspapi 的 data/tailnet.json：control_url 填 https://${DOMAIN}，admin_key 填同一把管理密钥。"
    echo "要备份：bash linbei/deploy/deploy.sh <服务器> ${DOMAIN} backup（数据库、noise 私钥）"
}

do_backup() {
    [[ -d "${VAR}" ]] || die "${VAR} 不存在，还没装过"
    echo "== 备份（headscale 会停几秒）"
    systemctl stop headscale
    tar -czf "${HERE}/backup.tgz" -C "${VAR}" .
    systemctl start headscale
    chmod 600 "${HERE}/backup.tgz"
    ok "已打包 $(du -h "${HERE}/backup.tgz" | cut -f1)"
}

"do_${MODE}"
