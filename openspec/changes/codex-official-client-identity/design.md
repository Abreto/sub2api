# Design

## Context

动机见 proposal.md。与方案相关的现状与关键证据：

- **UA 收口**：`enforceCodexIdentityHeadersWithUA`（`openai_codex_identity.go`）对 OAuth codex 出站无条件把 User-Agent/originator/version 重建为规范身份（`codex-tui/{version} (Ubuntu 22.4.0; x86_64) xterm-256color`）。这是当前最主要的假信号。
- **双身份系统**：`openai_codex_account_identity.go` 默认按 `(chatgpt_account_id, apiKeyID, kind, 客户端原值)` 派生全部标识（含 prompt_cache_key）；`openai_codex_fingerprint.go` 提供账号 opt-in 的 device/session/full 收敛（含 turn_id 头体同步生成）。二者在 installation/session/thread 上语义重叠。
- **v0.1.175 事故**：收敛默认开 → 额度缩水（#5553/#5555/#5556/#5582/#5610），回退即恢复。归因 caveat：当时 UA 同时是假串，无法完全剥离，但唯一有据可查的检测器开火在"超人设备"端（收敛），"设备数过多"端无事故记录。
- **installation-id 天然高基数**：`CODEX_HOME` 可任意新建，devcontainer/多机器的真人用户本就有多个安装；上游可用的强信号是 installation→多账号 关联图，而非单纯数量。
- **WS 池**：`normalizeOpenAIWSHandshakeCompatibility`（`openai_ws_pool.go`）仅在指纹收敛非 off 时把 installation/session 纳入连接分桶；off 时连接跨用户共享，握手头停留在创建者。ingress 已有 `ctx_pool/shared/dedicated/passthrough/http_bridge` 模式（per-account 可配），`dedicated` = 每下游连接强制新上游连接。
- **客户端门**：`codex_cli_only` 检测器（`openai_client_restriction_detector.go`）已具备 strict UA 前缀、originator 精确集、min/max 版本、引擎指纹 AND 门、黑/白名单，目前是账号 opt-in。
- **UA 收敛的引入历史（2026-08 考古，git 考证）**：收敛不是反指纹措施，是 2026-07 底上游容量降载事故的止血方案。时间线：02-07 `ce764bf2d` ForceCodexCLI（显式"伪装 UA"）→ 07-10 `8a51119e3` #3901 配对 404 → 08-02 `e1b76e224` 上游按 **originator 桶**降载（07-29 起 codex-tui 入桶、codex_cli_rs 正常，且 codex_cli_rs 配 curl UA 亦可正常返回——证明降载因子是 originator + 版本新旧，不是 UA 的 OS/arch/terminal 多样性），网关把 `server_is_overloaded` 误判为账号故障导致冷却风暴 → 08-03 `2eb24814f` 全员统一 canonical 身份 + 3h 自动同步版本 → 08-07 `dbb42881c` 默认身份 codex_cli_rs → codex-tui 再翻转（追着动态降载桶跑）。两个推论：① 保留真实 UA（OS/arch/terminal）+ 官方 originator + 较新版本不会比统一身份更触发降载；② 统一身份有单点脆弱性——身份一旦入桶即 100% 流量被降载，真实身份分布天然只部分中桶。
- **外部风控证据（CISA AA26-251A，2026-09-08，NSA/CISA/FBI 联合通告）**：通告描述前沿模型方对工业化蒸馏的四项检测指标，逐字为 "shared accounts from multiple IPs/user agents"、"24/7 sustained usage without human variation/idle periods"、"anomalous subscription-to-API usage ratios"、"new subscriptions immediately at maximum usage"。对位到 sub2api：① **多 IP/多 UA 共享账号是官方证实的检测分析**——per-account 的 UA 基数必须收敛到"人类多设备"量级且长期稳定（支撑 D8 的按规模分档与 class 派生 UA）；② 后三项是结构性信号（共享池全天候运转、订阅转 API、新号即满载），**身份工程无法消除**，属于产品形态固有暴露；③ 通告还点名"元数据净化"本身可检测（"previously consistent metadata 的突然消失"）——约束本方案：派生值但**保留字段形态**（不得整体丢弃 client_metadata/turn-metadata 等真实客户端会发的字段），且身份值必须**长期稳定**（禁止按请求/按天随机切换，部署切换是一次性 churn）。IP 侧（全部账号共享网关出口）为相邻工作流；TLS 指纹同为相邻工作流——OpenAI OAuth 主请求走 Go http.Transport 原生 TLS 指纹，与 codex 客户端（Rust/rustls）完全不同（issue #3624，账号级 TLS 伪装目前仅 Anthropic OAuth 生效），无视 UA 层伪装。OpenAI 官方已点名 sub2api 产品形态（订阅转 API 分享），社区证据（secrss 实测、腾讯云/TeamoRouter 指南）为次要佐证。
- **调度粘性**：`GenerateSessionHash` 用客户端原始 session 信号做 sticky，与账号侧派生天然兼容（派生在账号选定后进行）。


## Goals / Non-Goals

**Goals:**
- 上游视角下，每个伪设备 = 一台真实设备的"改号版"：UA 全真、单设备形态拟真、无跨账号关联。
- 每层身份基数有结构上界或结构拟真，且每档可独立回滚（信任档透传 = 总回滚位）。
- 三条出站路径（HTTP Forward / passthrough / WS）单一收口，一套派生实现。
- 每账号可观测设备基数 / churn / 每设备并发，用于持续验证假设。

**Non-Goals:**
- 不为非官方客户端做任何伪装或桥接（直接拒绝）。
- 不做 UA 归一化（含系统版本）。
- 不解决账号级聚合负载/用量的隐藏——那是容量问题，靠调度侧并发预算与加号解决，identity 方案不背这个目标。
- 不改动非 OpenAI 平台与非 codex 协议路径。
- **不做「每 UA 一对 OAuth 凭据」（2026-09 讨论，明确否决）**：真人多设备的完全一致形态是每机器独立 grant（各自登录、各持 token），但为伪设备复制该形态不划算——① 基数守恒：N 个 grant = 会话列表 N 个会话，把设备数信号原样搬到会话数信号；② 新 grant = 新设备登录，大概率触发邮箱/手机验证挑战，运营上极脆弱；③ N×token 生命周期与账号池调度/failover 交互复杂，且配额本为账号级、零容量收益；④ grant↔设备配对仅是弱间接信号（token session 线索），UA/installation/IP/用量才是直接观测面。拟真缺口由「每账号少量设备」量级下的 per-account profile + class 格子覆盖。

## Decisions

### D1：只放官方客户端，门默认化
- family 枚举：CLI = `codex_cli_rs`/`codex-tui`/`codex_exec`（同一二进制的交互/非交互形态）；Desktop = `codex_chatgpt_desktop` + `Codex ` 家族前缀；VSCode = `codex_vscode`/`codex_vscode_copilot`（显式产品决策：扩展是官方客户端，放行）。
- app-server：originator 是 `clientInfo.name` 逐字透传的任意串，按现有白名单（双因子 AND 引擎指纹）+ `AllowAppServerClients` 开闸放行；未列名 app-server 客户端在身份侧归入单一 `appserver` family（防止任意命名铸造派生基数），白名单列名者可独立成桶。
- 校验替代改写：UA 形态校验、originator↔UA 首段配对（#3901）、version ≥ per-family 下界（CLI 挂 `codexUpstreamMinVersion`，现有差异化文案"请升级 Codex"复用）。vscode/desktop 版本段语义不同源，先只做下界 + 形态校验，Max 卡太死会挡正常升级（现 policy 中 Max 本就可选）。
- 备选：继续 opt-in `codex_cli_only`。否决：伪装面（桥接整形）正是依赖"门不开也得能过"的存在。

### D2：UA 策略——Phase 1 采用格子收敛（2026-09 定稿，arch 按 OS 钉/留）
- **双维度论证（格子方案的立项理由）**：「真实」有两个独立维度——A 账号基数形态（一个 OAuth 多少条 UA）、B 请求内部一致性（UA 与请求体 OS 指纹对不对得上；codex 请求体内含 environment_context 的 cwd（/Users vs /home vs C:\）、tool_call_output 回放的 shell 输出等，UA 与内容矛盾在单个请求内部即可见）。全收敛 A 好但 B 破（所有用户的跨平台路径骑在单一 UA 下）；全透传 B 好但 A 破（UA 数=用户数，AA26-251A 信号 1）；格子唯一同时及格——基数 ≤24/典型 5-10（正常多设备人类），UA 跟随用户真实平台落格（内容一致）。
- 格子 = `(os × arch, family, version)`：**Mac 钉死 arm64、Windows 钉死 x86_64、Linux 保留用户真实 arch（x86_64/arm64）**——即 4 个 os×arch 组合 × 2 family × 3 version，上界 24，单账号活跃组合典型 5-10。钉死砍掉的是 Mac-Intel / Win-ARM 两个低人口格，HTTP 层不可验证、几乎不损失拟真；Linux 双 arch 保留（ARM 云主机/开发板在开发者人群真实大份额）。入站真实 UA 分类落格，出站 UA 由格子生成：
  - **CLI**：整串生成——`codex-tui/{用户真实版本(窗口内)} ({归一化 OS 串}; {格内 arch}) {terminal}`；terminal 仍按 `(account, os)` 从 seed 确定性定一个高人口值（freeform 字段防铸造，不保留用户值）；OS 版本归一化为「当前主流」，随发版更新表（含 os_info 三段归一化）。
  - **Desktop**：窗口内**重造而非透传**（kimi 审查加固）——版本段取客户端原值、OS 段归一化、terminal 槽钉死 `unknown`（实测唯一形态）、trailer 仅在 name==originator 且 build 号为数字点分时保留；家族白名单当前仅 `Codex Desktop`（唯一实测名），未实测名/小写变体回退 profile。禁止整串透传的原因：freeform 槽可被按请求铸造 UA 基数，override 包裹形态会保留第三方首段（#3901 错配）。
  - 版本段保留用户真实值（窗口内）；自动同步版本只用于回退流量。窗口 **[last-2, last+2]**（预发布按基版本判定；加上界的原因：自称未来版本（0.999.0）比陈旧版本更显眼，真实客户端领先同步值几个 minor 是常态所以留 +2 冗余）；下界复用上游硬下界 0.144.0。**已知缺口**：全局 `min_codex_version` 设置暂未并入下界（enforce 收口点无 policy 上下文，待门落地时一并解决）；拒绝文案用现成的「请升级 Codex」（当前出窗流量回退 profile 而非拒绝，拒绝门在 1b）。
- 超大共享账号的再收紧旋钮：Linux 的 arch 也降级为按 `(account, os)` seed 定一个（上界 →12，不改架构）。
- **一致性规则：UA 格子维度 = installation 派生 class 维度**（installation 按 `(family, os, arch)` 派生——同 D3 K-bucket class 定义；key 统一含 arch，Mac/Win 天然只有一值无需特判）；UA 含某可变维度而 installation 不含，会让同一 installation 跨该维度的多个值出现（不可能组合转移至设备层）。
- 备选：原样透传（不做格子）。保留给单租户账号档；共享账号默认格子收敛（AA26-251A 指标 1）。归一化系统版本曾被否决（浓度聚类论），格子方案复活它的原因是：每格 UA 由真实维度差异提供账号内多样性、由 account seed 提供 terminal 差异，不再是全网关同串——浓度担忧只适用于无差异的全局归一。

### D2-旧：UA 校验后原样出站（已被上方格子方案取代为默认，保留为单租户档）
- per-install 派生（D3）下，每个派生 installation 对应一个真实安装，其 OS 版本/arch/terminal/版本全部真实自洽：版本漂移=升级、terminal 变化=换终端、OS 版本变化=系统升级，均合法。
- 归一化的三个否决理由：全网关部署归一到同一 OS 串会制造不自然的浓度聚类（恰好与归一化的初衷相反）；desktop UA 形态杂、归一化规则维护成本高；归一化只有配"每平台完全收敛"时才必需，而收敛已降为非默认档。
- UA 字段是否进 installation 派生 key 的结论（讨论演进记录）：family 进（不同 app=不同安装）；app version 不进（升级不变 id）；OS 版本不进（原地升级）；platform/OS 种类+大版本进（换机器才算新设备；Win10/Win11 必须分设备）；arch 必须进（一台机器不可能换 arch，混用即异常信号）；terminal 不能进（freeform 可控字段，轮换即可铸造伪设备）。
- 备选：class 派生（family+platform+arch）为默认档。否决为默认：两个用户共享同一 class 时产生 OS 版本 flapping（一会儿 Win10 一会儿 Win11），且把 N 个用户的会话压进少数设备 → 每设备超人并发（v0.1.175 模式）。class 保留为 K-bucket 收紧档的组成部分。

### D3：installation-id 默认 per-(account, install_id) 派生，三档结构
- 默认档：`derive(account_seed, client_installation_id)`。设备形态完全拟真；跨账号 failover 不泄漏（account 在派生里）；无 flapping；无需解析 UA。
- 派生密钥复用现有 `codexFingerprintSeed`（每账号已有随机 seed），无新状态。
- 去 apiKeyID 的前提 = D1 的官方门：官方客户端 session/installation 均为 uuid，跨用户碰撞可忽略，apiKey 隔离失去必要性。
- 基数风险的立场（见 D8）：已知事故在收紧端；默认站在拟真端，用观测验证基数端，K-bucket 档兜底。派生是服务端纯函数，切档零协议成本。
- 信任档（透传）：单租户/信任下游；显式文档标注牺牲跨账号防关联。
- 收紧档（K-bucket）：`hash(class, install_id) mod K`，K≈4~8，class = family + OS 种类/大版本 + arch（OS 大版本必须入 key，否则 Win10/Win11 flapping）。设备数 ≤ 活跃class数×K，churn 被压平，每设备并发 = 该 class 用户数/K。
- 备选：每平台收敛为默认。否决：v0.1.175 实锤 + 单设备超人形态。

### D4：一次性 ID 透传
- turn-id、x-client-request-id 每请求/每 turn 唯一，不可能在两个账号重复出现 → 无跨账号关联面 → 透传。顺带删除现有"头体共享同一份收敛 IDs 以同步 turn_id"的机制（透传天然一致）。
- prompt_cache_key 仅在等于 body `client_metadata.session_id`（官方默认行为）时跟随 session 派生；显式自定义值按 `kind=prompt-cache` 独立派生（沿用现有语义）。

### D5：持久 ID 派生与缓存兼容
- session-id / thread-id / window-id / prompt_cache_key = `derive(account_seed, client_value)`，window 跟 thread 走（保留 `thread:N` 形态，不硬编码 `:0`）。
- 缓存命中链：sticky 调度按客户端原始 session 信号哈希（不变）→ 账号选定后派生 → 同会话回切同账号时派生值不变、缓存仍热；failover 到其他账号缓存必 miss，是"不跨账号关联"的固有代价，接受。
- 三路径单点收口：Forward（map 版）、passthrough（raw 版）、WS（header + payload）共享同一派生核心，沿用现有"staged IDs + map/raw 双实现"模式但仅一份语义。

### D6：WS 身份一致性
- compatibility key 无条件纳入 installation-id（off 档也不例外）→ 同桶 = 同一伪设备，修掉"握手身份停留在创建者"的不一致。UA 不进 key：上游对一条连接只见创建者 UA，同桶设备 UA 自然一致；进 key 会让连接数乘上版本数。
- WS ingress 默认 `dedicated`（已有模式，`ForceNewConn`）：最拟真形态（真 codex 每会话自开连接）；连接总量 ≈ 下游并发会话 ≈ 这些用户原生使用时的量。ctx_pool 保留给 HTTP 起源请求（Forward 选 WS transport 时无下游连接可对应）与资源优先场景。
- 备选：池化 + 桶 key。保留为非默认：一条连接跑多个 session、握手 session-id 归属他人，是真实 codex 不会出现的形态。

### D7：整形删除清单与必须保留清单
- 删（OAuth codex 路径）：instructions 合成（`defaultCodexSynthInstructions`）、functions/function_call 迁移、system message 提升、call_id 归一化的桥接分支、spark 图像桥接指令注入、账号 UA 覆写与 canonical UA 重建。
- 留：认证替换（Bearer/chatgpt-account-id/Host）、版本门槛校验、`guardOpenAICodexTurnStateEcho`、`store=false` 防御性强制、模型归一化中承担路由/计费的最小集、reasoning include 补齐（官方对齐，幂等）。
- 非 OAuth 平台不动：API Key 账号继续服务通用 OpenAI SDK 流量，桥接整形对该路径保留。

### D8：基数风险的观测优先
- 两个方向相反的信号不可同时消除：设备基数/churn（随用户量线性涨）与单设备形态（随设备数收紧而超人化）。已知检测器在后者开过火；前者无记录但「真实 UA + 不控基数」组合从未上过生产。**2026-09 外部风控报告修正**：UA/设备数量确为共享评分维度之一（非硬阈值，官方政策容忍多设备），故默认档按账号用户规模分化——多用户共享账号默认 K-bucket 收紧档且 **UA 一并按 class 派生为稳定值**（只收敛 installation 不收敛 UA，基数仍随用户数涨）；单租户/低用户数账号默认 per-install + UA 直通。档位可按账号配置或按活跃用户数自动选择。
- 故默认按账号规模分档 + 三指标观测（distinct installations / 新增设备速率 / 每设备并发会话）+ 告警 + 档位兜底。账号聚合负载的隐藏不在本方案目标内（调度侧并发预算已存在）。
- **相邻工作流（超出本设计范围，优先级可能更高）**：OpenAI OAuth 路径 TLS 指纹伪装（uTLS 模拟 rustls，对齐现有 Anthropic TLS profile 机制）；每账号独立出口 IP。

### D9：配置收敛
- 新单一 policy（OAuth 账号）：客户端门（family + 版本下界 + 引擎指纹开关）、installation 档位（default/passthrough/kbucket）、WS ingress 模式。
- 进入 deprecation：`ForceCodexCLI`、`gateway.disable_codex_identity_enforcement`、账号级 UA 覆写、`codex_cli_only`（被默认门吸收）、`codex_fingerprint_mode`（被档位吸收）。兼容期保留读旧配置映射到新 policy 的最接近档位。

## Phase 0：UA 专项修复（独立于完整重构，先行上线）

目标：每账号出站 UA 集合落在「人类多设备」区间——数量少、长期稳定、每条真实；同时消灭全网关共享串。AA26-251A 三条红线：① 身份值长期稳定（生成后钉死，禁止按请求/按天重随机）；② 保留字段形态（不因去整形而删除真实客户端会发的 header/字段）；③ 版本段继续走自动同步（profile 不含版本）。

- **0a. 每账号稳定 canonical UA（2026-09 按 AA26-251A 从部署级升级为账号级）**：canonical 出站 UA 改为 `weighted_pick(profile_table, sha256(codexFingerprintSeed))` 确定性派生的「机器 profile 后缀」——客户端家族固定 `codex-tui`，`(os; arch) terminal` 后缀从真实分布加权表选取（表 ~15-30 条高人口真实组合，权重≈真实流量占比，含 WSL 的 `(Ubuntu …; x86_64) WindowsTerminal`；生成器实现 os_info 三段归一化），版本段由自动同步重建、不进 profile。目标不是账号间唯一（碰撞合法且符合真实分布），而是：① 逃离单点（不再全员同串）；② 部署内账号群分布 ≈ 真实 UA 人口分布；③ 每账号稳定（同 seed 永远同 profile，零状态可重算，永不重随机）。无账号上下文的出站点用部署级 seed（生成一次钉死，持久化到内部 key）。附带 0c'（面板/账号 UA 保存时校验必须 CLI 家族）。配 kill-switch。
- **0b. 官方 UA 直通——移出 Phase 0，并入 Phase 1**：AA26-251A 证实「shared accounts from multiple IPs/user agents」为检测指标，共享账号上直通 = UA 基数随下游用户数线性涨，必须与档位策略（单租户直通 / 共享 class 派生）绑定决策，不适合作为独立先行步骤。
- 明确不做：全量放行真实 UA（等门 + 档位）；翻转 `disable_codex_identity_enforcement` 默认值（回到 8·3 事故前语义）；UA 归一化到系统版本级别（见 D2）。
- 已完成（零代码）：面板配置本部署独有真实 UA 作为过渡。
- 上线观测：`server_is_overloaded` 比率与上游错误率；接受一次性"换设备"churn。

## Open Questions（阻塞 tasks/specs，需实测或后续决策）

1. **vscode/desktop 与 cli 是否共享 installation-id**（是否共用 CODEX_HOME）：影响 K-bucket 档 class 语义与文档表述；不阻塞默认档。**已部分解锁（2026-09 实测样本）**：macOS Codex Desktop 真实 UA 为 `Codex Desktop/0.154.0-alpha.6.2 (Mac OS 26.6.2; arm64) unknown (Codex Desktop; 26.908.40834)`——独立 Desktop 的 originator 是 `Codex Desktop`（`Codex ` 家族，非 `codex_chatgpt_desktop`，后者属于 ChatGPT 桌面 app 内的 codex）；GUI 环境 terminal token 实锤为 `unknown`；尾部括号组是 clientInfo（app 名 + app build 号 26.x 形态，与引擎版本 0.15x 语义不同）。**已知缺陷**：canonical 组装链（`SetCodexUserAgentVersion`/`rewriteCodexUATrailerVersion`）会用 CLI 同步版本无差别覆盖 trailer 的 app build 号，产生自然界不存在的组合；desktop UA 应走直通分支原样透传，不做 canonical。UA 公式本身已由开源代码钉死（`{originator}/{引擎版本} ({os_type} {三段归一化版本}; {arch}) {terminal}`，os_info 3.14.0 语义化补零：15.5→15.5.0、22.04→22.4.0；WSL 下 WT_SESSION 透传使 `(Ubuntu …; x86_64) WindowsTerminal` 成为真实组合）。OAuth 与 API key 两种认证方式的 UA 同串（客户端构造与 auth mode 解耦，auth mode 只改变 base_url / Authorization / ChatGPT 专有头）。
2. **vscode 扩展 UA 形态与 installation 共享**：唯一剩余待实测项。
3. **K-bucket 的 OS 大版本粒度**：Windows NT 版本 10.0 同 cover Win10/Win11（build 区分），class key 取"OS 串归一化到已知枚举"还是"完整 OS 串"，需在实现时定（倾向：解析为白名单枚举，解析失败在门处拒绝）。
4. **灰度与一次性身份切换**：按账号比例灰度的具体机制（特性开关粒度：全局 / 分组 / 账号）。
5. **旧配置迁移映射表**：五个旧开关 → 新 policy 档位的精确映射与 deprecation 期长度。
