# 林北网络的 headscale fork

在上游 headscale 上只做四件事（用户隔离、固定管理密钥、客户端 DERP 列表、出口路由自动批准），改动尽量放在独立文件里，合并上游时冲突面最小。

| 改动 | 文件 | 碰到的上游文件 |
|---|---|---|
| 用户硬隔离 | `hscontrol/state/linbei_isolation.go` | `hscontrol/state/state.go` 一行（`NewNodeStore` 改用 `isolatedPeersFunc`） |
| 固定管理密钥（给 sspapi 用） | `hscontrol/state/linbei_admin_key.go`、`hscontrol/linbei_admin_key.go` | `hscontrol/state/state.go` 两处各一行（`ValidateAPIKey` / `AuthenticateAPIKey` 开头）、`hscontrol/app.go` 一行（`createRouter` 里 `h.loadLinbeiAdminKey()`） |
| 客户端 DERP 列表 | `linbei/derp-map.yaml` | 无，靠配置引用 |
| 出口路由自动批准 | `hscontrol/policy/linbei_exit_routes.go` | `hscontrol/policy/policy.go` 一行（`ApproveRoutesWithPolicy` 里 `canApprove` 先问 `linbeiAutoApproveExitRoute`） |

另外动了上游测试：

- `hscontrol/mapper/mapper_test.go` 的 `allow_all` 期望值从 2 改成 1（隔离后看不到别的用户）。
- `hscontrol/db/node_test.go` 的 `TestAutoApproveRoutes/2068-approve-issue-sub-exit-tag`：普通用户设备的期望里加上 `0.0.0.0/0`、`::/0`（上游只让 `tag:exit` 自动批准出口，fork 里设备宣告即批准，见第 4 节）；网段部分的期望不变。
- `hscontrol/servertest` 里前提就是「不同用户互相看得见」的测试，各加一行 `skipCrossUser(t) // Linbei fork`：`TestNodeAttrsUserTargetIsolated`、`TestPeerRemovedAsDelta`、`TestIssuesIdentity/cross_user_visibility_default_policy`、`TestIssuesMapContent/all_peers_have_user_profiles`、`TestIssuesMapContent/policy_change_carries_user_profile_for_new_peer`、`TestIssuesRoutes/via_exit_node_offered_to_members`（用户看不见运营方打 tag 的出口节点）、`TestPolicyChanges/policy_with_multiple_users`、`TestGrantPolicies/grant_per_user_isolation`、`TestViaGrantMapCompat`（回放的官方抓包混着多个用户和打 tag 的路由器）。它们原本覆盖的东西在 `hscontrol/servertest/linbei_isolation_test.go` 里换成同一用户内测：默认策略下两个用户互不可见、同用户互见且对端的用户资料照常下发，以及删节点按增量下发。合并上游后若有**新的**跨用户测试失败，照这个办法处理，并确认失败原因确实是「看不见别的用户」。

## 1. 用户硬隔离

上游的「谁能看见谁」完全由策略决定，策略写错或 `autogroup:self` 出回归，所有客户的设备名和家庭公网 IP 就会互相可见，而且数据一发出去就收不回来。fork 里不再依赖策略：

- 对端只在**同一个所有者组**里计算：同一用户的设备一组；所有打了 tag 的设备一组（运营方的基础设施，和用户设备互不可见）；没有用户的用户设备单独一组（失效关闭）。
- 组内照旧按策略算，所以策略仍然可以进一步收紧。**不配策略（默认全放行）也安全**：每个用户只看得见自己的设备。
- 客户端拿到的一切（对端列表、增量更新、用户资料、MagicDNS 记录）都从这份对端表派生，这里是唯一的关口。
- 按组计算还把上游「所有设备两两比较」变成了「每组内部两两比较」，用户再多也不会拖慢控制面。

测试：`go test ./hscontrol/state -run Isolat`（包括「隔离结果 = 上游结果 ∩ 同组」的对照，以及全放行策略下的具体断言）、`go test ./hscontrol/mapper -run TestBuildFromChangeVisibilityMatchesFullMap`（全量下发、增量补丁、用户资料都不越界）。

## 2. 固定管理密钥

sspapi 的 `tailnet.php` 要调 headscale 的 API（建用户、签授权 key）。上游的 API key 是 headscale 自己生成、存在数据库里的，headscale 整台没了就跟着没了，sspapi 也得跟着换。fork 里加一把**运营者自己定的固定密钥**，和管理员 API key 同等权限（v1、v2 两套 API 都认），同一个值也写在 sspapi 的 `data/tailnet.json`：重建 headscale 时只要用同一把，sspapi 一行不用改。

```yaml
linbei:
  admin_key_path: /etc/headscale/admin_key   # 文件里一行，至少 32 个字符（openssl rand -hex 32）
```

- 没配 = 功能关闭；配了但读不到或太短 = **启动失败**（不然 sspapi 每个请求都 401，没人会发现）。
- 比较是恒定时间的；`AuthenticateAPIKey` 对它返回一个没有所属用户的管理员 key，和命令行建的老式 key 一样。
- headscale 自己生成的 API key 照常能用，互不影响。

测试：`go test ./hscontrol -run Linbei`（配置解析）、`go test ./hscontrol/servertest -run TestLinbeiAdminKey`（按 `tailnet.php` 的调用顺序走一遍：查/建用户、签 key、列出、吊销；错的 key 一律 401）。守住 `state.go` 那两行接线的就是后一个测试：接线丢了它会失败。

> **DERP 节点名单不在这里**：早期版本在 fork 里做过 SingR 节点的注册表，2026-10-16 挪到了 sspapi 的 `tailnet.php`（sspapi 是永久在线的单台源站）。headscale 现在整台没了，DERP 中继网照常运转。

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

## 4. 出口路由自动批准

上游里设备宣告「我能当出口」（`0.0.0.0/0`、`::/0`）只是宣告，要管理员批准或策略 `autoApprovers` 放行才生效：共用的 tailnet 里，自称出口的设备能把别人的流量引到自己那里。我们这里用户已经硬隔离，出口只有同一账号的设备看得见，而且那些设备还得自己选它，**设备主人宣告就等于同意**。策略也表达不了这件事：`autoApprovers` 只认写死的用户名、group 和 tag，不认「每个用户」，而客户的用户是按需建的。

- **只批这两条出口路由**。普通网段路由（`10.0.0.0/8` 之类）照旧要策略或管理员批准，免得一台配错的设备悄悄把主人其它设备上的某个局域网段接管过去。
- 接线在 `ApproveRoutesWithPolicy`，state 里三处批准（注册、设备上报路由、策略重载）都经过它。已批准的路由照旧不会被自动收回。

测试：`go test ./hscontrol/policy -run TestLinbei`（无策略、有策略但没 autoApprovers 两种情况下出口批、网段不批；只认精确的 `/0`）、`go test ./hscontrol/servertest -run TestLinbeiExitNodeSelfApproves`（设备宣告出口后不经任何人批准，同账号另一台设备收到的 AllowedIPs 里就有 `0.0.0.0/0`、`::/0`，网段没有；别的用户看不见它）。两个测试都做过变异检查：去掉 `policy.go` 那一行接线就会失败。

## 合并上游后要跑

```bash
go test ./hscontrol/state -run Isolat -count=1
go test ./hscontrol/mapper -run TestBuildFromChangeVisibilityMatchesFullMap -count=1
go test ./hscontrol -run Linbei -count=1
go test ./hscontrol/servertest -run TestLinbei -count=1
go test ./hscontrol/derp -run Linbei -count=1
go test ./hscontrol/policy -run TestLinbei -count=1
go test ./hscontrol/servertest -skip "TestAPIv2|TestHAProberProperty" -parallel 32 -count=1   # 约 3 分钟。TestAPIv2* 要 nix 环境里的 tofu；TestHAProberProperty 是串行的属性测试，单它就 20 多分钟（上游 CI 也跳过），与隔离无关
```

`state.go` 里那一行如果因为上游重构挪了位置，要确认 `NewNodeStore` 传的仍然是 `isolatedPeersFunc`：传回 `policyPeersFunc` 不会编译失败，隔离却没了。`state` 包里的两个测试只测函数本身，**守住这一行接线的是 mapper 那个测试**：它经 `state.NewState` 建出真实的 State，全放行策略下期望只看见同用户的 1 台设备，接线丢了它会失败。

## 容器实测（2026-10-16）

- **隔离**：两个用户三台设备（客户端是 sing-box 1.14.3 的 tailscale endpoint），未配策略。同用户互通，跨用户双向不通；同一批客户端不重启，换成上游 headscale 后跨用户立刻互通，换回 fork 立刻断开。
- **DERP 注册表**（当时还在 fork 里，现已挪到 sspapi）：两台 SingR 报到后自动互相发现并 mesh，分别连在两台节点上的两个客户端经 DERP 双向收发成功；节点 `disable` 后另一台一次刷新内撤掉它；注册表宕机时节点重启靠缓存重建 mesh。

## 部署

`linbei/deploy/`：在 Mac 上一条命令完成编译、上传、安装 / 升级 / 备份 / 恢复。

```bash
bash linbei/deploy/deploy.sh root@<服务器> <域名> check                              # 只检查，不改服务器
bash linbei/deploy/deploy.sh root@<服务器> <域名> install --admin-key-file <文件>      # 首次安装
bash linbei/deploy/deploy.sh root@<服务器> <域名>                                    # 升级（沿用服务器上的管理密钥）
bash linbei/deploy/deploy.sh root@<服务器> <域名> backup                             # 备份到当前目录
bash linbei/deploy/deploy.sh root@<新服务器> <域名> install --admin-key-file <文件> --restore <备份包>   # 整台重建
```

- **前提**：Debian / Ubuntu x86_64（只编了 linux/amd64），能用 root SSH 登录（密码在终端里自己输，同一连接复用 10 分钟）；域名 A 记录指向服务器、**不开 Cloudflare 代理**（Cloudflare 不支持 tailscale 控制协议的连接升级）；**80 和 443 空着**、安全组放行。
- **做法**：headscale 直接对外，用内置的 Let's Encrypt 申请证书（官方支持的方式；FAQ 说他们不用反向代理、反代出问题只能靠社区）。
- **管理密钥**：本机一个文件，一行，至少 32 个字符（`openssl rand -hex 32 > admin_key`），和 sspapi `data/tailnet.json` 的 `admin_key` 相同，**抄进密码管理器**。上传后装到 `/etc/headscale/admin_key`（640 root:headscale），上传的临时目录装完即删。
- `check` 查：systemd、架构、curl、80/443 是否被占用、域名是否解析到本机公网 IP、有没有管理密钥。云厂商安全组查不到，要自己确认。
- `install` 先跑同样的检查，不过就什么都不改；过了才装：系统用户 `headscale`、`/usr/bin/headscale`、systemd 单元（`packaging/systemd/headscale.service`）、DERP 列表、`/etc/headscale/config.yaml`（每次重新生成，**手改会被覆盖**）、ufw 开着的话放行 80/443。然后启动、等 `/health` 用正式证书答通。
- **备份**：`backup` 让 headscale 停几秒，把 `/var/lib/headscale/`（数据库、`noise_private.key`、证书缓存）打包拉回本机当前目录。备份包里有 noise 私钥，妥善保管。
- **整台重建**：新机器 + 同一个域名 + 同一把管理密钥 + `--restore` 备份包，装好后把 DNS 指过去。设备和节点都不用动，sspapi 也不用改。没有备份包也能重建，只是设备记录没了、要重新加入。恢复时新机器上已有的数据会另存为 `/var/lib/headscale.before-restore-<时间>`。
- **升级**：改完代码重跑同一条命令。数据库、noise 私钥、管理密钥都保留。

线上：`https://hs.rickroll.top`（2026-10-16 部署）。

## 还没做

- 林北客户端接入组网（tailscale endpoint、DERP 中继组的探测与分流、TUN 下的路由）。
- 客户端拿授权 key、SingR 节点报到的接口都在 sspapi（`public/tailnet.php`），配置在它的 `data/tailnet.json`。
