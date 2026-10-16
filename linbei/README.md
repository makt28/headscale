# 林北网络的 headscale fork

在上游 headscale 上只做三件事，改动尽量放在独立文件里，合并上游时冲突面最小。

| 改动 | 文件 | 碰到的上游文件 |
|---|---|---|
| 用户硬隔离 | `hscontrol/state/linbei_isolation.go` | `hscontrol/state/state.go` 一行（`NewNodeStore` 改用 `isolatedPeersFunc`） |
| DERP mesh 注册表（给 SingR 节点用） | `hscontrol/linbei_derp_mesh.go` | `hscontrol/app.go` 两行（`createRouter` 里 `h.mountLinbeiDERPMesh(r)`） |
| 客户端 DERP 列表 | `linbei/derp-map.yaml` | 无，靠配置引用 |

另外动了上游测试：

- `hscontrol/mapper/mapper_test.go` 的 `allow_all` 期望值从 2 改成 1（隔离后看不到别的用户）。
- `hscontrol/servertest` 里前提就是「不同用户互相看得见」的测试，各加一行 `skipCrossUser(t) // Linbei fork`：`TestNodeAttrsUserTargetIsolated`、`TestPeerRemovedAsDelta`、`TestIssuesIdentity/cross_user_visibility_default_policy`、`TestIssuesMapContent/all_peers_have_user_profiles`、`TestIssuesMapContent/policy_change_carries_user_profile_for_new_peer`、`TestIssuesRoutes/via_exit_node_offered_to_members`（用户看不见运营方打 tag 的出口节点）、`TestPolicyChanges/policy_with_multiple_users`、`TestGrantPolicies/grant_per_user_isolation`、`TestViaGrantMapCompat`（回放的官方抓包混着多个用户和打 tag 的路由器）。它们原本覆盖的东西在 `hscontrol/servertest/linbei_isolation_test.go` 里换成同一用户内测：默认策略下两个用户互不可见、同用户互见且对端的用户资料照常下发，以及删节点按增量下发。合并上游后若有**新的**跨用户测试失败，照这个办法处理，并确认失败原因确实是「看不见别的用户」。

## 1. 用户硬隔离

上游的「谁能看见谁」完全由策略决定，策略写错或 `autogroup:self` 出回归，所有客户的设备名和家庭公网 IP 就会互相可见，而且数据一发出去就收不回来。fork 里不再依赖策略：

- 对端只在**同一个所有者组**里计算：同一用户的设备一组；所有打了 tag 的设备一组（运营方的基础设施，和用户设备互不可见）；没有用户的用户设备单独一组（失效关闭）。
- 组内照旧按策略算，所以策略仍然可以进一步收紧。**不配策略（默认全放行）也安全**：每个用户只看得见自己的设备。
- 客户端拿到的一切（对端列表、增量更新、用户资料、MagicDNS 记录）都从这份对端表派生，这里是唯一的关口。
- 按组计算还把上游「所有设备两两比较」变成了「每组内部两两比较」，用户再多也不会拖慢控制面。

测试：`go test ./hscontrol/state -run Isolat`（包括「隔离结果 = 上游结果 ∩ 同组」的对照，以及全放行策略下的具体断言）、`go test ./hscontrol/mapper -run TestBuildFromChangeVisibilityMatchesFullMap`（全量下发、增量补丁、用户资料都不越界）。

## 2. DERP mesh 注册表

每台 SingR 节点跑一个 DERP 中继，所有节点组成一个区域、两两 mesh。节点靠这个注册表互相发现，没有人需要维护名单。

接口（都要带节点 token，`Authorization: Bearer <token>`）：

| 请求 | 作用 |
|---|---|
| `POST /linbei/derp/register` `{"port": 8443, "pin": "<base64>", "host": "可选"}` | 报到兼心跳。地址默认取请求的来源 IP（经 `trusted_proxies` 解析），多 IP 机器可以手动指定 `host`。`pin` 是本机 DERP 证书公钥的 SHA-256（base64） |
| `DELETE /linbei/derp/register` `{"port": 8443, "host": "可选"}` | 主动离开（`singr derp disable`） |
| `GET /linbei/derp/mesh` | `{"psk": "<64 位十六进制>", "nodes": [{"host": "1.2.3.4:8443", "pin": "..."}]}`，只含过期窗口内报到过的节点 |

- **节点 token 不是 headscale 的 API key**：它只能调这三个接口，节点被攻破也碰不到用户、设备和授权 key。
- **PSK 第一次启动时生成**，和名单一起存在一个 JSON 文件里（权限 600、原子写），不加数据库表、不加迁移。重启后 PSK 和名单都保留：PSK 一变，所有节点会掉出 mesh。
- 超过过期时间（默认 24 小时）没报到的节点自动移出名单；再次报到立即回来。

配置：

```yaml
linbei:
  derp_mesh:
    node_token_path: /etc/headscale/derp_node_token   # 或 node_token: "..."，至少 32 个字符
    state_path: /var/lib/headscale/linbei_derp_mesh.json   # 默认放在 noise 私钥旁边
    expiry: 24h
```

生成 token：`openssl rand -hex 32 > /etc/headscale/derp_node_token && chmod 600 /etc/headscale/derp_node_token`。没配 token 时整个功能关闭、路由不注册；配了但读不到（文件不存在、太短）直接启动失败，避免节点在不知情的情况下没有 mesh。

⚠️ **state 文件里有 PSK，要和 headscale 的数据库一起备份**；丢了会生成新的，所有节点在下一次拉取时自动换上，期间 mesh 会断几分钟。

测试：`go test ./hscontrol -run LinbeiDERPMesh`。

## 3. 客户端 DERP 列表

`linbei/derp-map.yaml` 是客户端收到的 DERP 列表：一个区域，DERP 节点是虚拟域名 `derp.linbei.internal`（客户端经代理隧道访问，节点改写到本机回环），外加几个只做 STUN 的公共服务器。headscale 配置：

```yaml
derp:
  server:
    enabled: false            # 不用内置 DERP：它公网直连，绕过「只给有效订阅用、流量计费」
  urls: []                    # 不用官方 DERP 列表
  paths:
    - /etc/headscale/linbei-derp-map.yaml
```

测试：`go test ./hscontrol/derp -run Linbei`（字段名写错会被解析成零值，这个测试守着）。

## 合并上游后要跑

```bash
go test ./hscontrol/state -run Isolat -count=1
go test ./hscontrol/mapper -run TestBuildFromChangeVisibilityMatchesFullMap -count=1
go test ./hscontrol -run LinbeiDERPMesh -count=1
go test ./hscontrol/derp -run Linbei -count=1
go test ./hscontrol/servertest -skip "TestAPIv2|TestHAProberProperty" -parallel 32 -count=1   # 约 3 分钟。TestAPIv2* 要 nix 环境里的 tofu；TestHAProberProperty 是串行的属性测试，单它就 20 多分钟（上游 CI 也跳过），与隔离无关
```

`state.go` 里那一行如果因为上游重构挪了位置，要确认 `NewNodeStore` 传的仍然是 `isolatedPeersFunc`：传回 `policyPeersFunc` 不会编译失败，隔离却没了。`state` 包里的两个测试只测函数本身，**守住这一行接线的是 mapper 那个测试**：它经 `state.NewState` 建出真实的 State，全放行策略下期望只看见同用户的 1 台设备，接线丢了它会失败。

## 容器实测（2026-10-16）

- **隔离**：两个用户三台设备（客户端是 sing-box 1.14.3 的 tailscale endpoint），未配策略。同用户互通，跨用户双向不通；同一批客户端不重启，换成上游 headscale 后跨用户立刻互通，换回 fork 立刻断开。
- **DERP 注册表**：两台 SingR 用 `singr derp enable --control ... --token ...` 报到后自动互相发现并 mesh，分别连在两台节点上的两个客户端经 DERP 双向收发成功；节点 `disable` 后另一台一次刷新内撤掉它；headscale 宕机时节点重启靠缓存重建 mesh。

## 部署

`linbei/deploy/`：在 Mac 上一条命令完成编译、上传、安装 / 升级。

```bash
bash linbei/deploy/deploy.sh root@<服务器> <域名> check   # 只检查，不改服务器
bash linbei/deploy/deploy.sh root@<服务器> <域名>         # 安装或升级，可重复执行
```

- **前提**：Debian / Ubuntu x86_64（只编了 linux/amd64），能用 root SSH 登录（密码在终端里自己输，同一连接复用 10 分钟）；域名 A 记录指向服务器、**不开 Cloudflare 代理**（Cloudflare 不支持 tailscale 控制协议的连接升级）；**80 和 443 空着**、安全组放行。
- **做法**：headscale 直接对外，用内置的 Let's Encrypt 申请证书（官方支持的方式；FAQ 说他们不用反向代理、反代出问题只能靠社区）。不经反代，节点报到时 headscale 直接看到来源 IP，不用配 `trusted_proxies`。
- `check` 查：systemd、架构、curl、80/443 是否被占用、域名是否解析到本机公网 IP。云厂商安全组查不到，要自己确认。
- `install` 先跑同样的检查，不过就什么都不改；过了才装：系统用户 `headscale`、`/usr/bin/headscale`、systemd 单元（`packaging/systemd/headscale.service`）、DERP 列表、`/etc/headscale/config.yaml`（每次重新生成，**手改会被覆盖**）、节点 token（只在第一次生成）、ufw 开着的话放行 80/443。然后启动、等 `/health` 用正式证书答通，最后打印节点 token 和给 sspapi 用的 API key。
- **API key 只在第一次安装时生成并打印一次**（标记文件 `/var/lib/headscale/.linbei_sspapi_key_created`），重复执行不会再建新 key。丢了就在服务器上 `headscale apikeys create -e 3650d` 再建一把，旧的用 `headscale apikeys expire --prefix <前缀>` 作废。
- **升级**：改完代码重跑同一条命令。数据库、noise 私钥、DERP mesh 状态（PSK）、节点 token、API key 都保留。
- **要备份**：`/var/lib/headscale/`（数据库、`noise_private.key`、`linbei_derp_mesh.json`）和 `/etc/headscale/derp_node_token`。noise 私钥丢了所有设备要重新登录；mesh 状态丢了会换 PSK，节点要重启 SingR 才能重新互联。

线上：`https://hs.rickroll.top`（2026-10-16 部署）。

## 还没做

- 林北客户端接入组网（tailscale endpoint、DERP 中继组的探测与分流、TUN 下的路由）。
- 客户端拿授权 key 的接口已在 sspapi（`public/tailnet.php`），各 API 域还要放 `data/tailnet.json`。
