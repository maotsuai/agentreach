# AgentReach P0 之后开发执行手册

文档版本：1.4 · 2026-11-06

适用基线：P0 commit e4cd040bd0b02275fb8642f59d218a864c2541a6

基线分支：codex/orbstack-execution-plane

状态：P1 目标已实现，通过完整检查、原生验收与最终独立审计，相关文档和阶段证据已同步。P2 实现、完整检查、原生/真实 harness 验收与独立复审均通过，且已按后续授权推送本人 fork。P3 实现、完整复验及独立复审通过，交付止于 Luna 本地阶段提交，不自动推送；P4–P5 尚未启动。P3 证据见 [验证记录](validation/P3.md)，P2 见 [验证记录](validation/P2.md)，P1 见 [验证记录](validation/P1.md)。

本文为后续 Sol 实现提供工程规格、任务拆分、验证门槛和交接格式。它不能保证实现绝不出错；它要求在不确定处停止依赖该结论的工作，通过实验和独立审计闭环，禁止用猜测、降级或改低验收标准消除错误。

初版编写时仅获授权编写规划文档。用户于 2026-11-05 确认完整 P1 方案并要求开工，用户于 2026-11-06 又明确授权在 P1 推送 fork 且 P2 可行性审查通过后开工 P2；条件已满足。用户于 2026-11-06 在 P2 推送本人 fork 后又明确授权新 session 完整执行 P3；当前实施授权止于 P3，不自动 push 或进入 P4/P5。后续阶段仍由 composer 提交范围、主要改动、影响、验证和回退方案，获得明确确认后实施。已确认阶段内的常规实现细节不重复请示。本文中的后续拟新增接口和示例不是现有命令，也不预先授予运行 Docker、发布端口、迁移状态或删除资源的权限。

## 阅读与执行索引

1. [实际基线与代码入口](#1-实际基线与代码入口)
2. [必须保持的约束](#2-必须保持的约束)
3. [阶段依赖与交付清单](#3-阶段依赖与交付清单)
4. [工作流、环境与提交](#4-工作流环境与提交)
5. [公共状态和请求契约](#5-公共状态和请求契约)
6. [P1：Workspace 与 PathMapper](#6-p1workspace-与-pathmapper)
7. [P2：Policy、Host Capability 与安全审计](#7-p2policyhost-capability-与安全审计)
8. [P3：持续终端](#8-p3持续终端)
9. [P4：服务、受控编排与端口](#9-p4服务受控编排与端口)
10. [P5：恢复、升级与并发加固](#10-p5恢复升级与并发加固)
11. [跨阶段验证矩阵](#11-跨阶段验证矩阵)
12. [P0 回归清单](#12-p0-回归清单)
13. [需先实验的技术问题](#13-需先实验的技术问题)
14. [Sol 工作指令与交接模板](#14-sol-工作指令与交接模板)
15. [验收、审计与 Luna 提交模板](#15-验收审计与-luna-提交模板)
16. [回退与故障处置](#16-回退与故障处置)
17. [蓝图追踪与最终交付](#17-蓝图追踪与最终交付)

## 1. 实际基线与代码入口

### 1.1 权威来源与已完成范围

按以下顺序判断“已经支持什么”：

1. 当前 Git commit、实际代码和该版本的真实测试结果。
2. [MANAGED-PROJECTS.md](MANAGED-PROJECTS.md) 的当前 P0 + P1 + P2 使用契约；历史 P0 能力以指定基线和实验记录为准。
3. [RESEARCH.md](RESEARCH.md) 的指定版本实验记录，尤其 2026-11-01、2026-11-05 的修正。
4. 用户提供的《AgentReach 二次开发架构蓝图：OrbStack 执行平面 v1.1》，作为目标而非已实现 API。
5. 本文的后续设计选择；如果与新的真实证据冲突，先修订设计，不能偷偷改变实现或安全承诺。

原蓝图位置：/Users/nyan/Downloads/AgentReach_二次开发架构蓝图_OrbStack_v1.1_2026-10-31.md。跨机器交接时应另行提供原文件；本文已整理阶段需求，不要求实现者猜测缺失附件。

P0 已实现：

- 为**已存在且运行的** OrbStack Docker 容器显式登记不可变项目绑定。
- 固定 Docker CLI、config、context、endpoint、daemon ID、container ID、image ID、StartedAt、挂载与完整 HostConfig 快照。
- 原生 macOS 身份、OrbStack 实际 data_dir、外置卷 UUID/挂载点/实际路径归属及哨兵检查。
- 执行、文件访问、复用的 helper/pool 和 stdin 通道的检查；一次运行时检查失败永久废弃该 exec-server 连接。
- 严格 JSON 控制状态、session schema v2、managed 绑定摘要、损坏状态不降级。
- Claude/Codex 已安装真实二进制的严格启动前探测、受控配置与环境；真实 Codex 原生 apply_patch 和 cwd 验证。
- 明确拒绝尚未实现的 managed TTY、Guest signal，以及未验证 harness。

P0 **没有**：

- managed 目录级 FileOps 策略、任意 Shell 的项目目录沙箱。
- 类型化通用 Host 能力、强制安全审计、审批授予和撤销。
- 持续 terminal_id、受管后台服务、端口登记和代理。
- create/start/restart/rebind/reconcile、自动迁移和自动修复。

现有配置是严格 JSON。不要把原蓝图概念 TOML 拿来当配置加载器输入，也不要无必要地新增第二套格式。

### 1.2 代码定位表

下表的“当前”以本手册顶部 P0 适用基线为准。P1 增量实现与验证见 [P1 记录](validation/P1.md)，不要据此把 P0 当作当前候选的完整能力描述。

| 文件/包 | 当前职责 | 后续改动约束 |
| --- | --- | --- |
| internal/project/project.go | Spec、Binding、Record，登记与加载 | Schema=1；Load 当前要求 Revision=1、Generation=1。不能只把计数加一就声称支持重绑定 |
| internal/session/managed.go | 整个 Record 的摘要、当前 session 关联、可信控制路径 | 新状态必须保持旧 child/连接不能因同名 session 恢复而改投目标 |
| internal/session/session.go | TransportGuard、FileOpsGuard、workspace/cwd 等 | 共享同一个失败锁存回调，不能在底层另开未锁存检查路径 |
| internal/runtime/docker/docker.go | 固定 Docker 连接、只读 inspect | P4 如增加 mutation，增加类型化方法；不公开任意 Docker argv 给模型 |
| internal/runtime/orbstack/guard.go | 外置存储、容器身份、挂载和权限检查 | 保持普通运行路径只读；service 的允许端口不能放宽 workspace 容器约束 |
| internal/runtime/orbstack/volume_darwin.go | 原生卷身份与实际路径检查 | Linux 测试注入检查器不代表原生 macOS 验证 |
| internal/hostpath/ | Host 实际文件身份、祖先与私有文件检查 | 复用，勿以字符串小写或 filepath.Clean 替代文件身份 |
| internal/state/ | 严格解析、单文件原子替换、有界目录锁 | 当前不是多记录事务引擎；不要把多个 Write 当成事务 |
| internal/fileops/ | POSIX/Python/helper、多级选择、连接池和 guard | P1 必须明确各 tier 的约束能力，不能失败后落到无目录限制的 tier |
| cmd/reach-helper/main.go | 现有 Guest 文件协议 helper | 当前未提供 P1 根目录约束；新增协议须保持 unmanaged 兼容 |
| internal/execserver/execserver.go | RPC、mapPath、runtimeReady、连接生命周期 | 当前绝对 Guest 路径可透传；mapPath 返回 string 不足以表达 P1 拒绝 |
| internal/execserver/fs.go | 文件/目录/句柄/copy/walk/canonicalize | 路径策略须覆盖所有入口，不能仅修 read/write |
| internal/execserver/process.go | 单次 process/stdin、取消与输出 | 不把 process_id 当持续 terminal_id；保持 P0 signal/tty 拒绝，除非新实现有真实证据 |
| cmd/reach/managed_harness.go、harnessguard.go | 严格参数、可执行文件、配置、探测 | 禁止用 --force、宽松 MCP、Host shell shim 绕过新功能接缝验证 |
| cmd/reach/managed_cwd.go | Claude 窄范围 Host cwd 记录 | 不是一般 Host 文件写能力 |
| internal/harnessprobe/ | 离线 seam 探测 | 新工具入口需单独验收，保留原 Bash/fs 的真实启动测试 |
| internal/audit/audit.go | 可关闭、失败不阻断的旧操作日志 | 不是 P2 的强制授权审计；不要直接冒充或改变 unmanaged 契约 |
| test/conformance/managed/main.go | 真实 harness + localhost mock | 扩展真实工具调用，不用手写 RPC 成功替代实际 harness 验收 |
| examples/orbstack/ | Dockerfile、Compose、project JSON | 是显式准备模板；不能被 health/inspect 自动执行 |

新增包按需要建立，勿提前创建空的 router/policy/terminal/service/ports 框架。先读当前调用链，再确定最小改动位置。

### 1.3 P0 证据边界

P0 的末次记录包括 Guest 检查、Linux/Darwin lint、Linux/arm64、Darwin/arm64、Windows/amd64 交叉构建，以及真实 macOS Claude 2.1.220、Codex 0.148.0 的离线 mock 验收。末次产品候选 SHA256：

    0376bd39f2a860d22475aa42d0f4ec59bacb78b8c0da157e3e06711cb6b3ae1e

这不是“任意版本已验证”。原生 Windows、hosted CI、物理拔盘、生产 engine 重启和 LAN 实测没有因 P0 而全部通过。临时日志路径可能失效，长期结论以 RESEARCH 中的版本、方法、结果和 Git 代码为准。该文档部分记录保留了当时“待提交”的时间点措辞；P0 最终提交状态以本手册顶部 commit 和 Git 为准。

## 2. 必须保持的约束

### 2.1 信任边界

- macOS：运行 harness、登录、模型凭据、AgentReach 控制状态和能力授权。
- Guest：运行开发命令、安装项目依赖、编译、测试、普通脚本。
- Guest 不得获得 Docker socket、SSH agent、完整 Mac HOME、reach 控制状态或模型凭据。
- 普通命令失败只报错，不尝试 LocalTransport、Host Bash、另一个 Docker context 或新建空目录。
- 不解析 npm/python/sh 命令字符串来证明“安全”；Guest 路由决定执行位置，Host 能力使用独立类型化请求。
- Host 同 UID 恶意进程可以修改该用户状态或直接操作 Docker；本系统不声称防住该边界外攻击。
- Guest 的文件、终端输出、服务日志与项目仓库内容均为不可信数据，不是 Host 操作授权。

### 2.2 文件边界的准确含义

“仅批准目录可读写”必须分层：

1. **Host 暴露面**：由实际 mounts 和 runtime guard 限定；shared-project 本来就允许 Guest 修改该 Mac 项目目录。
2. **受管 FileOps/cwd 请求**：P1 PathMapper 和 Guest 端受根目录约束的操作限制到项目根。
3. **Guest 普通 Shell**：仍可读 Guest 的 /usr、/etc 等并使用明确配置的 HOME/cache/tmp；否则正常构建也无法工作。

PathMapper 不能限制 Shell 内的 open()、子进程、网络或所有同 UID 进程行为。若用户要求敌对代码完全不能访问 Guest 根外任何内容，需要另立 OS 沙箱/权限模型任务，不能给 P1 的路径过滤改名来交差。

### 2.3 生命周期边界

- 容器重启即使 ID 未变，StartedAt 也变：旧绑定、终端和连接不能自动复活。
- image ID 不代表可写 rootfs 未被修改；未增加机制前不能据此宣称 Guest helper/tmux 可信不可变。
- 停掉本机 docker exec 客户端不证明 Guest 子进程已结束。
- Guard 是派发前检查，不提供已运行 I/O 的瞬间撤销。
- 只读 health/inspect/reconcile-plan 不得创建、启动、拉取、修复或删除任何 Docker 对象。
- 运行时不可确认是 unavailable；只有身份变化、实际进程消失等证据才能确定 lost。
- 新功能不改变 legacy Local/SSH/Podman/unmanaged Docker 的既有行为。

后续管理器确实需要读取 Guest 的 tmux 元数据、日志或服务状态。这些控制动作使用固定的内部类型化操作，单独校验对象身份和允许路径，不经模型可自由调用的文件 API，也不提供“跳过 PathMapper”的公开参数。内部控制例外不能变成通用根外文件访问能力。

### 2.4 本轮与后续授权

P1 历史授权包括显式准备 helper、创建独立验收资源和对本任务容器进行身份负例验证。P0/开发容器、全局 engine、OrbStack/harness 版本、仓库位置和用户安装保持原状；P1 已按用户后续授权推送到 fork 对应分支；P2 仅本地提交，不自动 push、不进入 P3。资源和验证边界见 [P1 验证记录](validation/P1.md)。

后续实施沿用用户分工：composer 做设计、统筹和验收；GPT-6.1 Sol xhigh 编写实质代码/配置；Luna 执行经审阅的 commit。重要代码通过测试后，由未参与实现和调试的代理独立审计。额度不足时记录准确中断点，不以不断新建代理掩盖同一个失败原因，不擅自切换实现模型。

## 3. 阶段依赖与交付清单

主顺序：P1 → P2 → P3 → P4 → P5。每个阶段可以分多个工作批次，但**阶段完成、验证、审计后才做阶段 commit**；不要把后阶段代码混进前阶段。若阶段过大需要分成交付里程碑，先让用户确认拆分，再按每个已批准里程碑提交。

| 阶段 | 可交付结果 | 前置依赖 | 完成后仍不承诺 |
| --- | --- | --- | --- |
| P1 | 双 workspace 模式，受根目录约束的 FileOps 与 cwd，所有 native fs/patch 接入 | P0 固定绑定、guard、真实 harness 测试 | 任意 Shell 目录沙箱、Host API |
| P2 | 决策器、结构化能力调用、精确审批、撤销、持久安全审计、受控 harness 工具桥 | P1 路径语义 | service/port 已实现、任意 Xcode |
| P3 | tmux terminal_id、多轮交互、增量输出、重连、准确 lost 状态 | P2 请求/授权/审计，P1 cwd | 容器重启保留 REPL、完整二进制 PTY |
| P4 | Compose sidecar、临时服务、loopback 端口管理、可用 open_url、显式资源创建 | P2 授权与审批、P3 日志/临时终端能力 | 默认 LAN 暴露、任意 Compose 执行 |
| P5 | 显式重绑定与迁移、reconcile、故障恢复、升级和备份还原演练 | 所有资源有身份和状态 | 自动恢复进程内存、任意破坏性修复 |

关键依赖处理：

- P2 定义 open_url 的真实授权与执行器，但地址只能来自经验证的 exposure。P4 端口注册尚未落地时，生产调用返回 exposure_unavailable；P2 用注入的登记器测试执行器，不伪造“WebUI 已闭环”。
- P2 定义 port_expose 的 capability 名称和授权数据结构，生产 driver 未接入时明确 unsupported。P4 才实现动作。
- P2–P4 不修改现有项目 schema=1、固定 revision/generation=1 的绑定语义。独立 policy、terminal、service、exposure 记录引用原 Record 摘要。
- P4 service 使用独立容器/记录，不给原 workspace 追加端口或修改固定 HostConfig。
- 如 P3 所需 tmux 不在已登记镜像里，不在 health 中安装；使用显式准备的新测试容器和新项目 ID。既有项目原地升级/重新绑定留给 P5。
- Xcode/Simulator 是后置可选范围，默认不纳入 P1–P5 的首轮交付；新增需独立能力规格和授权。

## 4. 工作流、环境与提交

### 4.1 启动一个阶段前

1. 确认用户已授权具体阶段，记录本次范围和停点。
2. 查看 git status、HEAD、分支与未提交改动。不得 reset/clean 用户内容；有无关改动则隔离工作并明确归属。
3. 阅读本手册对应阶段、代码入口、相关测试、现有 AGENTS 规则。
4. 重核外置卷和 Guest。历史名称只是查找线索，不能凭名称直接修改资源。
5. 先列出请求入口 → policy → guard → transport/FileOps/driver → 真实副作用的调用链和错误链。
6. 对第 13 节所列适用技术问题做最小实验，保存成功与失败证据。
7. composer 冻结本阶段细化方案，交 Sol 实现；如出现实质架构/授权变化，先修订并确认。

### 4.2 环境事实与重新发现

| 项目 | P0 历史值 / 使用规则 |
| --- | --- |
| 仓库 | /Volumes/MPM/sth/agentreach；不要因全局默认目录不同而自动迁移 |
| 外置普通资源 | /Volumes/MPM/MCACHES/agentreach |
| 需 APFS 的资源 | /Volumes/APAP/MCACHES/agentreach；实际 OrbStack 数据另由正式 Storage 管理 |
| OrbStack 实际 data_dir | P0 为 /Volumes/APAP/vm/Orbstacks；每次读真实配置并检查，勿写死该值 |
| 开发容器历史名 | agentreach-dev-terra-20261031-7f3c |
| 容器源码目录 | /workspace/agentreach；.git 在开发容器只读挂载 |
| 开发 UID | 10001；依赖缓存已外置，先 inspect 确认而非重新创建 |
| Go | go.mod 要求 1.25.8，P0 在 Linux/arm64 使用 1.25.8 |
| lint | Makefile 指定 golangci-lint v2.12.2 |
| Docker context | orbstack；生产执行还需固定 endpoint/config/CLI 和 daemon ID |
| OrbStack 版本 | P0 实测 2.2.4；蓝图建议与当前安装可能不同，升级前另做兼容验证，不自动升级 |

只读调查可以在 Host；依赖安装、go fmt/vet/test/build/lint 全在 Linux Guest。macOS 原生验收只运行 Guest 交叉构建的候选，不在 Host 建一套开发依赖。

以下仅为**未来已授权阶段的模板**，DEV_ID 必须填入当次检查过的完整容器 ID：

~~~sh
docker --context orbstack inspect "$DEV_ID"
docker --context orbstack exec "$DEV_ID" \
  bash -c 'cd /workspace/agentreach && go version && go env GOCACHE GOMODCACHE GOPATH'
docker --context orbstack exec "$DEV_ID" \
  bash -c 'cd /workspace/agentreach && make check'
docker --context orbstack exec "$DEV_ID" \
  bash -c 'cd /workspace/agentreach && make lint'
~~~

使用 bash -c，P0 的 login shell 会丢失必要 PATH。运行前确认容器 ID、挂载、命令和 PATH 符合实际，不把模板当自动探测脚本。不在 Host 执行 make install；不运行会花真实模型额度的 make e2e，除非另获授权。

交叉构建输出到已验证的外置构建目录；设置明确 GOOS/GOARCH/CGO_ENABLED，给候选记录 SHA256。受沙箱限制时按工具机制申请最小权限，不能改写路径绕过限制或静默落到内置盘。

### 4.3 测试与证据节省规则

- 每次修复先重验受影响路径；阶段末运行项目必需检查。
- 没有新改动、环境变化或疑点，不反复运行相同全套测试。
- mock、实际 Guest、原生 macOS、真实 harness、真实 LAN、原生 Windows 分开记录。
- 用离线 mock 测真实 harness，不把版本字符串或手写协议响应当接缝证据。
- 日志保留命令、退出码、commit/工作树差异摘要、二进制哈希、环境版本、资源 ID、期望与实际。
- 不记录 token、auth.json、完整真实环境或可能含密钥的参数；只用 dummy credential/canary。
- 新测试资源随机命名并加任务标签；清理只匹配本次记录的精确 ID，绝不按宽泛名称 prune。
- 不运行已有固定名称 Docker 清理脚本对着用户 daemon；先审脚本再使用。

### 4.4 阶段冻结与 commit

实现自查 → 受影响测试 → 阶段验收 → 独立审计 → 核对问题 → 修复并重验 → 冻结 diff → Luna commit。

Luna 只 stage composer 给出的精确文件清单，不使用 git add -A 吞入别人的改动；不改代码、不跳过 hooks、不 amend 旧阶段、不 push。检查提交 parent、文件清单和提交后工作树。建议主题：

- P1：feat(workspace): constrain managed filesystem operations
- P2：feat(policy): add typed capabilities and approval audit
- P3：feat(terminal): add persistent tmux sessions
- P4：feat(service): manage services and loopback exposures
- P5：feat(runtime): add explicit reconciliation and recovery

主题应以最终实现为准；不能在未达验收时用“已提交”代替完成。

## 5. 公共状态和请求契约

以下为拟新增契约，可调整具体 Go 类型名称，不可悄悄改变语义。

### 5.1 身份与权威状态

| 对象 | 最少绑定信息 | 权威来源 |
| --- | --- | --- |
| Workspace | project_id、完整 record_digest、mode、guest_root、批准 Host 映射 | 登记 Record + 当次 guard |
| Policy | schema、policy_revision、project_id、record_digest、能力白名单与限制 | Host 私有 policy 记录 |
| Approval | approval_id、规范请求摘要、policy_revision、record_digest、期限、使用次数、撤销状态 | Host 独占审批存储 |
| Terminal | terminal_id、project_id、record_digest、daemon/container ID、StartedAt、后端实例 nonce、tmux session/pane、日志 epoch/cursor | Host 身份记录 + Guest 实测状态 |
| Service | service_id、spec_digest、record_digest、driver、资源精确 ID、operation_id、generation、健康/运行状态 | Host 期望状态 + Docker/Guest 观察 |
| Exposure | exposure_id、service_id、service generation、container ID、endpoint、实际监听地址和端口、代理实例身份 | Host 管理器实际 socket/受管 proxy + 记录 |

不可用容器名、PID、tmux session 名、IP 或端口中的任意一个单独证明身份。对象 ID 使用不可预测随机值，非路径，严格长度/字符检查；跨项目查询与操作拒绝。

P2 以单项目 schema=1 安全 ledger 权威保存 policy/pending/approval/operation/event，审批消费与 intent 同次提交（具体接口与预算见 [POLICY.md](POLICY.md)）；P3/P4 新增独立对象 schema。每个记录都复用严格解析、原子写与锁，新增敏感目录加入 hostpath/CheckControlPaths 的保护集合。对 JSON 重复键、未知字段、负数/溢出/超长值、额外 JSON 文档做拒绝。

### 5.2 请求执行顺序

~~~text
解析严格结构化请求
→ 读取 Host 权威 project/policy/对象记录
→ 固定请求对象与原始绑定，不从 Guest 返回值重新决定授权目标
→ 路径/参数规范化
→ policy 判定 guest / host-safe / host-approval / deny
→ 必要审批精确匹配并检查撤销/期限
→ 取得相关资源锁，再检查 revision/generation/批准状态
→ runtime guard 与对象实际身份检查
→ 持久记录执行 intent（P2 起受管副作用要求）
→ 派发类型化动作
→ 记录实际结果、状态和不确定性
→ 返回结构化结果
~~~

无外部副作用的纯解析可以先于 guard；任何实际文件/进程/网络动作不能先做再审批。授权层返回的参数必须原封不动进入执行器；执行器不再接收一个可被替换的原始请求。

审批消费与 intent 使用同一串行化约束，跨进程竞争最多一个执行者。Host 写盘成功与 Docker 副作用之间没有分布式事务：崩溃中间态必须保留 pending/unknown，通过 operation_id 和实际资源重新核对；不能宣称 exactly-once。

### 5.3 状态写入与并发

- 单文件可用 state.Write；多记录变更需要操作日志/单一权威记录/可恢复提交标记，选择最简单足够方案。
- 每个 phase 先定义锁粒度和顺序。推荐按 project → resource → journal 排序，避免两条路径反向拿锁。
- 不持有全局锁等待用户、长时间网络请求或进程输出；审批展示在锁外，消费时重新检查。
- 读路径不 mkdir。损坏、悬空链接、新 schema、残留锁不能被解释为空状态。
- stale lock 不自动抢占；维护命令需展示原资源/进程证据和具体 repair 方案。
- 不依赖普通文件 O_APPEND 的“4 KiB 一定原子”假设保证多进程安全；新安全日志需要明确 writer 串行化、长度帧/完整性检测及 fsync 策略。

### 5.4 错误、预算与取消

保留 P0 现有 runtime 错误类别；为新层新增有区分度的类别，如 path_denied、approval_required、approval_expired、approval_revoked、audit_unavailable、resource_lost、output_gap、unsupported、outcome_unknown。新增名需在接口文档与测试中冻结，不在多个层随意换词。

所有输入都设上限：请求体、stdin 帧、输出帧、并发对象数、日志保留量、查询耗时、锁等待。具体默认值在阶段规格中确定并测试临界值。参数缺失或超限明确拒绝，不以截断执行内容维持“成功”。

取消与关闭分清：

- 停止本次等待。
- 关闭客户端通道。
- 向 Guest 前台进程组传递交互中断。
- 终止受管资源并确认。

不能用一个 cancel=true 代表四者均完成。

## 6. P1：Workspace 与 PathMapper

### 6.1 目标、范围与选择

P1 交付受管文件 API 和工作目录的明确项目根约束，覆盖 shared-project、guest-owned；复用 P0 mounts 校验。默认只允许项目根内的 managed FileOps。工具链读 /usr、缓存等通过 Guest 普通命令完成，不在 P1 暗增任意 FileOps 根。

选用 **Host 做映射/授权，Guest 在实际文件操作时持有项目根目录句柄进行约束**。仅在 Host 调 realpath，然后用路径字符串发到 Guest 不够；两次操作之间可被 symlink 交换。

推荐扩展现有 Go reach-helper 的 managed rooted 模式，用 Go 1.25.8 实际提供的根目录操作能力。实施前核实目标 API 对读、创建、rename、递归删除和 symlink 的语义；缺失部分用经过验证的 Linux descriptor-relative 实现补足。不要未经验证假定所有 os.Root 方法都存在或都满足要求。

managed rooted helper 是 P1 FileOps 强制要求。缺少该能力返回 unsupported/明确准备说明，不降级到没有根目录约束的 POSIX/Python tier。unmanaged tier 选择维持原状。helper 的安装必须显式准备，不在普通读/写失败后偷偷编译安装。

Guest helper 运行在 Guest，不能提供比容器权限更强的敌对同 UID 隔离保证。日志/测试必须区分“协议请求不会路径穿越”与“恶意 Guest 已控制 helper/内核”；后者不靠该协议解决。

### 6.2 任务顺序

| 批次 | 实现内容 | 必须先证明 |
| --- | --- | --- |
| P1-A | 请求矩阵、PathMapper 类型与表驱动测试 | 每种输入所属路径命名空间；不修改 Shell 文本 |
| P1-B | rooted helper 协议、能力协商与根目录持有 | symlink 竞态、创建/rename/remove 不逃根，旧 helper 拒绝 |
| P1-C | FileOps 包装器、pool/句柄、exec-server 全入口接入 | 任何 tier/pool/旧句柄都不能绕过 |
| P1-D | exec/process cwd 与 Claude cwd 接入，Guest rooted launcher 持有 cwd 后执行 | 非法 cwd 和检查后 symlink 交换均不使 body 从根外启动 |
| P1-E | 真实 Claude/Codex shell/fs/native patch、双模式验收 | 含 /var ↔ /private/var 协议别名及根外负例 |
| P1-F | 文档、审计、修正、commit | P0 guard/legacy 不回归 |

### 6.3 映射算法

定义 PathRequest 至少包含 operation、path namespace、path、必要的 second_path 和请求 cwd。namespace 由受信调用入口确定，不能让模型随意声称一个路径已批准。

**构造前置约束（已获用户确认）**：Host 协议别名与 Guest 根任一是另一的严格路径分量前缀时，返回 `path_denied`；完全相等允许，`/project` 与 `/project-other` 等分量兄弟允许。检查注册的 shared `host_path`，以及 harness 控制 cwd 的原始、规范路径等每个可信别名。不能通过改前缀优先级猜测意图，否则同一 file URI 可能在后续请求指向另一文件。拒绝发生在受管派发前；已有记录仍可只读 inspect/health，不改 schema、不自动迁移或重绑定。应显式准备 `/workspace/project` 等独立 Guest 根，绑定改变时登记新项目。

1. 检查编码、NUL、长度、URI scheme/authority、重复解码和未知字段；file URI 只按协议解码一次。拒绝 remote authority、查询和 fragment 等不支持形态。
2. **Host 协议别名**：只映射已验证的 harness 控制 cwd 原始/规范路径及项目明确共享路径。使用分量边界，/a/proj-other 不能匹配 /a/proj。
3. **Guest 绝对路径**：用 POSIX path 语义，必须落在 guest_root；不得因 Host 的 filepath 在不同平台上表现不同而改变 Guest 语义。
4. **相对路径**：FileOps 默认相对登记 guest_root，保持当前协议行为；process cwd 则按单独入口契约处理。不得把 session 的可变 cwd、harness Host cwd 和 FileOps 根混用。
5. 在词法规范化前保留足够上下文识别 ..，规范化后再次做根分量检查。空路径按每个操作明确是根还是无效，不统一猜测。
6. Host 协议映射只执行一次：原始 CLI 使用 Protocol；exec-server 映射 URI 后，底层 FileOps、pool、派生路径和句柄固定使用 Guest，reopen 保持该命名空间。得到 root-relative 路径交给 rooted helper；最终实际操作仍在 Guest 根句柄内完成，不信任预检 realpath 的短暂结果。
7. 返回规范 Guest 路径用于显示；不把 Guest 路径直接传给 Host os.Open。

Host 大小写别名和硬链接问题继续使用 internal/hostpath；Guest 路径不要一律 lower-case。guest-owned 没有 host_path，严禁为了 IDE 便利创建虚构 Host 镜像目录或通过 ~/OrbStack 视图旁路文件操作。

### 6.4 每种文件操作的规则

| 操作 | 规则 |
| --- | --- |
| Read/Stat/Hash | 根内解析；跳出根的 symlink 拒绝；范围和大小边界保持现有契约 |
| Write | 父目录通过根句柄解析；在同目录创建独占临时文件后原子替换；不跟随根外叶子 symlink |
| Mkdir | 每级在根内创建；不调用 Host MkdirAll 映射补目录 |
| Remove recursive | 不删除项目根；不穿越 symlink 指向目录；根句柄下遍历，不调用任意路径 rm -rf |
| Rename/Copy | 源和目标都独立授权；跨根拒绝；先检查全部路径再做第一项副作用 |
| List/Walk/Search/Glob | 遍历不逃根；匹配表达式不能当 shell 片段；取消、深度、结果数与输出大小受限 |
| Canonicalize | 只给出允许的 Guest 规范路径；根外不得通过 canonicalize 获得可用文件句柄 |
| Open/ReadBlock/Close | 句柄绑定 root identity + project digest + 连接；每次使用经过共享 guard；失败后只允许清理 |
| native apply_patch | add/update/delete/move 涉及的所有路径受同一约束；任何失败不误写 Host 控制目录 |

Go 原子 rename 替换文件避免截断另一个硬链接目标，但不能解决所有硬链接读取语义。P1 必须明确策略：managed 入口遇到多硬链接普通文件默认拒绝受约束的内容读写，避免路径在根内却读写根外同 inode；单独记录对工作流影响。普通 Guest shell 仍可自行操作这些文件。若要放宽，应先给出精确可信来源和范围证明再确认变更。

helper 主协议应增加明确的 rooted capability/version 握手及 root 标识。root 参数由可信 Host 会话提供，不能让单个 fs 请求改变。helper 标准输出只写协议帧，诊断走 stderr；超长帧在分配内存前拒绝。

新 helper 的生产准备路径先沿用显式安装/镜像准备，不使用现有“找不到 helper 就在 Host 本机 go build”的潜在便利路径。由开发 Guest 构建 Linux helper，记录哈希并放入明确批准的镜像或 Guest 工具目录；验证 helper 的查找路径和现有自校验。自校验不能证明同 UID 的 Guest 环境不可篡改，文档不得扩大此结论。

### 6.5 cwd 与 Shell

受管 process 的 cwd 必须映射并验证为允许根内目录。失败必须在任何 env export 或 command body 前退出。不要为了路径策略重写命令字符串中的 /Volumes、~、$HOME 或参数。

入口约束必须闭合到**实际启动进程**：使用一次性的 Guest rooted launcher，从项目根句柄安全解析并持有 cwd 的目录句柄，在同一启动链内进入该目录并直接 exec 用户命令。不能先 helper realpath/stat 验证，再让独立 shell cd 字符串或 Docker -w 重新解析路径。具体 fd-relative 解析、fchdir/exec 或等效机制必须在目标 Linux 上实测；不得在共享 FileOps/helper 服务进程中改变全局 cwd。

检查最终 Transport 拼接出的执行请求，防止已通过 launcher 后又追加一层按原始路径 cd 的 shell 前缀。argv、env 和 quoting 保留既有语义，所有 managed exec/process/Claude Shell Prefix 入口使用同一启动约束。验收持续交换 cwd 或父目录 symlink，检查命令实际 pwd 和根外 canary，必须拒绝或从已安全取得的允许目录实例执行，不能从攻击者替换的根外目录启动。

这里固定的是成功取得的目录实例，不承诺阻止其他 Guest 进程随后重命名该目录，也不限制用户程序启动后的 chdir；不得把启动约束描述为完整 Shell 文件系统沙箱。

Claude 的 cd 行为需要新增明确语义：若一次普通 Guest Bash 自行 cd 到根外，该命令仍是 Guest 行为；不能声称已经被 cwd 入口约束阻止。下一次要持久化/使用根外 session cwd 时拒绝，不把错误 cwd 写入 Host bookkeeping。若要限制命令运行中的 chdir，那是独立 OS 沙箱需求。

### 6.6 P1 验收

- 双 workspace 模式的全部 FileOps 正常路径、只读 mount、相对/绝对路径、Unicode、空格、长名、合法 Host 别名。
- Host 别名与 Guest 根双向严格嵌套在派发前拒绝；相等和分量兄弟允许；覆盖原始/规范控制 cwd、canonical Guest URI 跨请求复用、拒绝时零文件或命令 body。
- ..、根前缀碰撞、URI 编码、根外 symlink、父目录 symlink 替换竞态、悬空链接、多硬链接、递归删除和双路径操作。
- warm pool、旧文件句柄、直接 helper 请求均不绕过；旧 helper 或 capability 不匹配明确拒绝。
- 真实 Codex native apply_patch 根内新增/更新/删除；根外和 move 越界无 Host 副作用。
- 真实 Claude Bash 读写 Guest、非法入口 cwd 不执行 canary；原窄 Host cwd callback 不扩大权限。
- cwd/父目录在检查与启动之间发生 symlink 交换，不从根外目录运行 body；覆盖实际 Transport 拼接和原生 harness 入口。
- 缺失外置卷或 runtime 身份变化拒绝，恢复后旧连接仍废弃。
- unmanaged FileOps 套件、P0 回归、Linux 检查与 Darwin 原生 conformance 通过。

交付文档须明确新 helper 的准备方式、旧项目是否需要重新准备镜像、允许/拒绝案例及限制。不在无测量情况下宣称性能提升。

## 7. P2：Policy、Host Capability 与安全审计

### 7.1 目标与模块

新增 internal/policy 负责规范请求、决策、审批、消费和撤销；internal/host 负责固定能力执行器。现有 cmd/reach 和 harness 只适配请求，不各自复制一套授权逻辑。

如需要 router，优先用一个小的纯决策模块，不引入“任意命令 → 猜测 Host/Guest”的规则系统。Guest 是普通开发操作默认路由。

首版能力：

| 能力 | 输入 | P2 生产状态 |
| --- | --- | --- |
| open_url | project_id、exposure_id、限定相对 path | 执行器可交付；没有有效受控 exposure 时拒绝，P4 才完成 WebUI 闭环 |
| port_expose | service_id、guest_port、loopback 地址、可选端口 | 可判定权限和记录审批，driver 尚未实现则 unsupported |
| runtime/service 变更授权 | 明确 operation/spec digest | P4/P5 才接入实际执行器 |
| Xcode/Simulator | 后续独立规格 | unsupported，不能通过 argv 兜底 |
| arbitrary_host_shell / 网络配置 / 密钥读取 | 无 | 固定 deny |

### 7.2 审批流程与防重放

请求规范化后计算 request_digest。摘要内容至少覆盖：能力名、project_id、record_digest、policy revision、对象 ID/generation、完整规范参数、允许副作用、资源范围。不能只对展示用摘要或截断字符串签名。

流程：

1. plan/request 生成 Host 私有 pending 请求，返回 request_id 和完整可审阅差异；这不执行动作。
2. 操作者通过独立 Host 控制入口 approve，看到精确项目、目标、参数、影响、有效期、次数。Guest 无该入口，harness 工具白名单也不暴露 approve。
3. approval_id 不是泛用 bearer shell token；仅能匹配原请求，默认一次使用，默认短期限。期限采用什么时钟、重启/时间回拨如何拒绝，须明确测试。
4. 执行时在锁内重新加载 policy、撤销状态、runtime 和资源 generation；参数任何变化都需重新计划，不能复用旧批准。
5. durable intent 和 approval 消费后再 dispatch。崩溃后“已消费但结果未知”不自动补执行。
6. revoke 阻止将来使用，不伪称终止已经发生的外部动作；终止需要单独受管动作。

不把 stdin 输入一个 y、环境变量 APPROVED=1、Guest 文件、LLM 文本“用户说可以”当可信审批。Host 同 UID 能绕过控制入口的限制需如实说明；本方案防模型可用工具和 Guest 伪造，不声称多用户权限隔离。

### 7.3 安全审计

旧 internal/audit.Append 可关闭、写入失败不阻断，只保留给旧操作日志。新增 managed 安全 journal 必须返回错误；受管副作用在 durable intent 写入失败时拒绝执行。REACH_NO_AUDIT 不得关闭该安全路径。

事件至少包含：schema/event_id/time、project_id、record_digest、policy_revision、request_id、operation_id、actor 类型、capability、规范请求摘要、approval_id、决策理由码、目标身份、phase(intent/result)、结果/错误类别。

不要默认写完整 shell、env、终端输入、URL query、数据库口令或 auth。展示字段采用白名单；摘要不等于隐私保护，低熵秘密不应裸 hash 入日志。用户需要的终端/服务输出另行保留并注明可能含敏感内容，不能和授权 journal 混存。

并发多 writer、半写、磁盘满、fsync 失败、恶意 symlink/hardlink、权限变化都需测试：

- intent 未持久化：零 dispatch。
- 动作已发生、result 写入失败：返回 outcome_unknown/audit_unavailable，保留 intent，要求 reconciliation；不能报告“动作没发生”。
- journal 尾部损坏：记录完整性问题，不像旧 audit.Read 一样静默跳过后继续声称完整。
- 可选 hash chain 只能辅助检测，不能声称能阻止同 UID 攻击者重写整条链。

### 7.4 Host 能力执行器

open_url 不接受任意 URL 字符串：由 exposure_id 查出当前健康且同项目的 loopback origin，拼接经过验证的相对路径。拒绝 scheme/authority 改写、userinfo、控制字符、file/javascript/data scheme、反斜杠和编码后的 authority 绕过。打开前重核 exposure generation。

调用原生固定可执行文件并传 argv，禁止 sh -c。URL 是单个参数且不能成为选项。浏览器之后的重定向/导航不受该入口全面控制，不能承诺“打开后永远只访问 loopback”。

P2 的 Host 调用通道推荐一个最小 stdio MCP/结构化 CLI adapter，复用同一 policy executor。如果选择 MCP：

- Claude strict MCP 从“空配置”变为“只包含固定可信 bridge”，不是允许项目自带 MCP。
- Codex controlled home 只写该 bridge 的固定配置；不给任意 server command 配置入口。
- bridge 的二进制、配置、cwd、PATH 都执行 P0 可信路径检查，不放到 Guest 可写 bind。
- stdout 只承载协议，日志走 stderr，限制请求大小并拒绝未知能力。
- 增加真实 Claude/Codex mock-model 工具调用 conformance，同时验证普通 Bash/fs 仍进 Guest。

bridge 不是通用 Host shell，也不能 expose approve/revoke 的操作者身份入口给模型。

### 7.5 P2 验收与收口

精确参数审批成功；修改项目/路径/端口/spec/generation 后旧审批失败；并发重放至多一个 dispatch；过期、撤销、时间回拨、损坏审批拒绝。journal 失败零 dispatch，result 失败准确表达不确定性。

未实现的 capability 明确 unsupported；未知字段和任意 shell 固定 deny。真实 harness 只能调用批准 bridge，不能加载仓库或命令行注入的其他 Host 工具。P0 路由检查和 P1 FileOps 套件不回归。

P2 完成报告需写明 open_url 尚缺 P4 exposure 来源，不能宣称后台 WebUI 已交付。批准规格/日志格式/撤销语义及 operator 使用流程必须随代码提交。

P2 实际接口及状态格式以 [POLICY.md](POLICY.md) 为准；实施授权和真实 harness 接缝审查保留在 [交接](P2-IMPLEMENTATION-HANDOFF.zh-CN.md)。

## 8. P3：持续终端

### 8.1 支持范围

首版为 tmux 文本交互终端；稳定 terminal_id 跨 Agent/Host 客户端重启存在，Guest 容器运行期间保留 REPL。普通 exec-server process 继续原有语义，不自动转换成持久终端，不给所有 docker exec 加 -t。

拟新增 API：

~~~text
terminal.create(project_id, cwd, argv[], cols, rows, request_id)
terminal.write(terminal_id, input_id, bytes)
terminal.read(terminal_id, cursor, max_bytes)
terminal.resize(terminal_id, cols, rows)
terminal.interrupt(terminal_id)
terminal.inspect/list/attach/close(...)
~~~

首版 interrupt 只承诺交互式 Ctrl-C 的已测行为；任意 signal 未验证则 unsupported。bytes 的文本/控制字符支持范围通过实验冻结，NUL/二进制/粘贴限制显式报告，不能默默损坏。

### 8.2 身份、隔离和状态机

tmux 使用 AgentReach 专用 socket 和每终端随机名字，禁止连接用户已有默认 tmux server。记录 socket 标识、tmux session/pane ID、nonce、container ID、StartedAt。使用固定 tmux 可执行路径和受控配置；不加载项目的 tmux.conf。

建议状态：

~~~text
creating → running → exited → closed
                ↘ unavailable → 重新检查后 running 或 lost
creating/running → lost（确知容器 generation 或 pane 身份失效）
任意可关闭状态 → closing → closed / outcome_unknown
~~~

终态 lost/closed 不复用 ID。runtime 暂时连接失败不自动恢复老 exec-server 通道；terminal 的新的 attach 调用可以在新 guard 和同一真实身份核对后连接仍存活的终端，这是新连接，不是撤销 P0 废弃锁存。

create 先持久 intent，再创建指定后端，最后记录实际 pane；超时检查本次 nonce 资源，不盲重试 create。若成功后 Host 记录失败，保留可发现的精确 orphan；不清理整个 tmux server。

### 8.3 输入与 argv

create 的 cwd 用 P1；argv 是数组，若后端必须接收 shell 字符串，在一个集中函数里逐参数 quote，并通过特殊字符/空参数/换行/选项注入测试。不能直接 strings.Join。

write 不拼接 send-keys 命令文本。根据实际 tmux 版本选择 literal 模式或 load-buffer + paste-buffer，固定 argv；Enter/Ctrl-C 等控制动作独立建模。input_id 去重应持久化“已接受/发送中/已发送/未知”状态：

- 重复已确认输入返回原结果，不重复发。
- 发送后崩溃且无法确认，返回 unknown，不自动重放，避免 REPL 命令重复副作用。
- accepted 仅表示后端接收，不表示应用已执行成功。

并发输入同一终端必须串行；resize 和 close 也检查身份与锁，不让延迟请求打到复用 pane/PID。

### 8.4 输出与 cursor

capture-pane 仅供快照或调试，不能用作无损增量日志。正常输出来自 pipe-pane 日志，并给每条流定义 epoch 和单调 byte offset；API cursor 可为 opaque 字符串，内部至少编码 terminal instance、epoch、offset。

推荐实现约定：

- 每次终端创建产生不可复用 log epoch，原始字节日志与显示渲染分离。
- 使用启动屏障：先建 pane 并等待，接好 pipe-pane 后才释放用户程序，避免遗漏最初输出。必须实测退出很快的程序。
- read 返回 bytes/base64、next_cursor、state、earliest_available_cursor 和可选 gap 原因；max_bytes 有上限。
- pipe-pane 收集的是 PTY 输出，stdout/stderr 通常合流；不得虚构独立来源或“与程序原始 stdout 完全相同”。
- 同 cursor 重读在保留范围内返回一致字节；UTF-8 多字节分割由客户端渲染处理，不随意修改原数据。
- 首版采用有界单终端日志；达到限额后按明确策略停止收集/关闭终端并标记 output_limit，或经过审查实现带 epoch 的分段轮转。不得无声覆盖或假称没有丢字节。
- Guest 重启、日志丢失/截断/身份异常、collector 失败要显式 gap/lost；不能把 offset 置零继续当旧流。

推荐为 pipe-pane 使用一个小的固定 Guest collector（可在现有 helper 新增独立模式），而不是拼接包含模型输入的 shell 重定向。collector 负责字节计数、限额、错误/结束标记和必要 flush；不能引入 Mac 密钥、Host socket 或通用远程控制入口。创建后等 collector-ready 再释放程序；pane 退出后先确认 collector 已排空并持久化最终 offset，再报告“全部输出已读完”。关闭时记录是否完整 drain；日志目录和文件使用随机实例身份并拒绝链接。这个有限日志组件不等于完整 PTY supervisor，不能顺带扩大 P3 功能。

日志默认在 Guest 专用终端数据位置，且无需长期 Host 轮询也能继续收集。Host 只保存 cursor/身份和必要状态。Guest 同 UID 能篡改输出日志，日志不是防篡改证据。需要强认证输出/严格任意字节流时再设计独立 supervisor，不能边实现边无限扩张 P3。

### 8.5 关闭、重连与清理

Host CLI 退出、Agent 一轮结束不得关闭终端。attach 重新校验 project digest、runtime generation、tmux 实例和 pane，不凭“同名存在”恢复。

close 只针对精确 terminal；返回实际退出或 unknown。进程后代是否全部结束必须实测；无法确认时不能报告彻底终止。不能 kill tmux 默认 server、容器全部进程或不相关 terminal。

新 Terminal Manager 的失效行为不能削弱 P0：一旦某一调用连接 guard 失败，那个连接的通道永久废弃；对象记录可保留为 unavailable 等待新连接检查。

### 8.6 P3 验收

- Python/其他 REPL 连续至少 10 次跨调用输入输出，变量状态保留。
- 完整退出并重启真实 Claude/Codex，重用 terminal_id 继续交互。
- Host CLI/bridge 重启而容器不重启，输出仍被收集，可从 cursor 补读。
- 快速首行、长输出、无换行输出、ANSI、UTF-8 分片、输出限额、collector 故障均有准确结果。
- 同 cursor 重读、非法 cursor、旧 epoch、并发输入、重复 input_id、不确定重试。
- resize 使用实际 PTY 观测；Ctrl-C 在 REPL 的行为实测；未支持的 signal 明确拒绝。
- 停止/重启随机测试容器，旧 terminal lost，原内存不可恢复；同名新容器无法继承。
- 多 terminal 隔离、精确 close、孤儿记录和 runtime 暂时不可用场景。

若无损增量日志的启动/限额设计无法实现，P3 不得声称完成；先缩小公开契约并让用户确认，或提交新的 supervisor 方案。

## 9. P4：服务、受控编排与端口

### 9.1 分层与实施顺序

新增 internal/service、internal/ports；在 runtime/docker 增加受限 mutation 接口。先完成 Compose sidecar 和固定 loopback 发布，再做 workspace 内临时服务和动态代理，最后打通 open_url。

| 批次 | 输出 |
| --- | --- |
| P4-A | ServiceSpec、ExposureSpec、状态机、审批计划和 Docker mutation guard |
| P4-B | Host 生成的受限 Compose 清单、sidecar create/start/status/logs/stop |
| P4-C | 已发布 loopback 端口的登记、实际检查、open_url |
| P4-D | workspace 内临时 dev server 及有身份的进程监管 |
| P4-E | 长寿命 localhost 代理、冲突/重启/关闭和孤儿检测 |
| P4-F | 实际 WebUI、Agent 重启、LAN 负例、故障注入、独立审计与提交 |

workspace 容器在整个 P4 仍使用 P0 固定快照；服务额外挂载/网络/端口必须有**服务专属的**显式安全规格，不能让 workspace Guard 允许任意 ports/mount 变更。

### 9.2 ServiceSpec 最小字段

schema、project_id、driver、image 的已解析不可变 ID/digest、argv、cwd、非敏感 env、精确 mounts、网络 ID、非 root UID、资源限制、healthcheck、restart policy、日志限额、批准的 guest ports。

Host secrets 默认不支持；需要数据库密码等时单独设计限定用途和交付，不能把整个 Host env 或 env_file 注入。healthcheck 属于 Guest 执行动作，不是 Host shell。

服务状态区分 created/starting/running/unhealthy/stopped/lost/unavailable/outcome_unknown。running 不代表 ready；服务可无 healthcheck，但不得因此声称 HTTP 就绪。

operation_id 与 spec_digest 固定同次 start/restart；重试先查已经创建的精确对象。Docker 名称与 Compose label 可帮助发现，最终操作按精确 ID。

### 9.3 Compose 安全编排

拒绝把 Guest/仓库提供的任意 compose.yaml 直接交给 Host docker compose。该文件可修改 mounts、privileged、network_mode、build、env_file、include 等，等价于扩大 Host 权限。

首版选择从已批准类型化 ServiceSpec **在 Host 私有目录生成**最小 Compose 文件，固定 compose project 名、文件路径、工作目录和环境，不加载仓库 .env、自动 override、include、extends、profiles 或任意 build。规范化后的渲染结果摘要进入审批，实际执行前再核对。

只允许：

- 固定镜像与明确服务名、项目专属标签。
- 批准项目目录或批准 named volumes；禁止 Host 控制/凭据/socket 路径。
- 非 root、cap_drop ALL、no-new-privileges、无 devices/privileged/Host namespace。
- 明确的隔离网络；无 host network。
- 显式 loopback ports；不允许短语法默认 0.0.0.0 发布。
- 明确资源上限和 restart policy，首版 manual/no。

首次创建应分为“生成与审核 → create 但不 start → inspect 实际 mounts/HostConfig/labels/network/image → start → readiness”。若实际配置不符，保持未启动并返回差异；只对本次精确自有资源给出清理计划。不能先 up 运行未知镜像，再以事后 inspect 宣称副作用前已验证。镜像自带 VOLUME、ENTRYPOINT、USER、healthcheck 和 Compose 默认行为也要展开到最终运行规格；发现未批准匿名卷或命令变化则拒绝。

sidecar 优先加入 workspace 已存在且明确批准的项目网络；不通过 docker network connect 临时修改原 workspace 来绕开固定绑定。网络不符合需要时，返回显式环境准备/新项目计划，不削弱 P0 guard。多个服务网络间的访问范围也进入 ServiceSpec，不默认加入所有项目的网络。

build/pull 不是 ensure_ready 的副作用。缺少镜像返回 image_unavailable；如要提供 pull/provision，是单独有审批、磁盘检查和摘要的动作。Compose 启动参数要核实实际版本是否支持 no-build/no-deps/pull 控制，不能假定旗标可用。

不要在每次 service.start 重新执行整个 workspace Compose 项目。只启动已批准服务；不连带拉起 dependencies。stop 不运行 down -v，不删除用户 volume/network。固定版本的 Compose CLI 及插件执行链和配置路径应纳入可信路径验证。

### 9.4 workspace 内临时服务

临时 npm run dev 等在 workspace 内，由独立 ServiceDriver 记录身份、日志和健康状态；可复用 P3 的 tmux 实现组件，但不能直接把用户终端一概当服务。

不使用 docker exec ... & / nohup 作为完整监管。临时服务不暴露交互 write；若需要调试输入，明确转换为 terminal 的新操作。服务退出、容器重启、workspace binding 失效时如实报告；不凭旧 PID 重启/kill。

服务状态依赖指定 Guest TCP/HTTP readiness。probe 在 Guest 或可信 Host 网络路径执行，URL/port 受登记范围约束，不成为任意 SSRF 探测器。

### 9.5 Port Manager

exposure 必须绑定 service_id、service generation、container ID、实际目标地址与 guest_port；由 policy 决定 allowed path：

1. 固定 sidecar 发布：创建时使用明确 127.0.0.1:port:guest_port，并对实际 inspect 的所有 PortBindings 复核。
2. 动态代理：运行中容器不能假设可以追加 -p；选用 Host 的最小 loopback TCP proxy，并让它有独立于当前工具调用的生命周期。
3. .orb.local/Guest IP：单独的访问路径，首版不当成“已关闭外界访问”。记录 allowed/unsupported/unknown 的实际策略与验证结果。

动态 Host proxy 推荐复用一个按需启动的最小 Host manager（可为新增 reach-hostd）。只在需要长寿命代理时运行，不因为普通 Bash 而常驻。它只转发已批准服务端点，不提供任意 connect(host, port)、HTTP CONNECT 或 Host shell。

proxy 用 net.Listen 在 127.0.0.1:0 原子分配端口并**持有同一个 listener**，不能“探测空闲端口后关掉再启动”制造竞态。自动端口写入实际值。默认只支持 IPv4 loopback；若支持 ::1，单独审批/测试，不能绑定 ::。

Host manager 使用私有控制 socket、受控权限和实例 nonce；每个请求核对项目/审批/资源身份。禁止只凭 PID 文件 kill。Host manager 崩溃则标记 exposure unavailable，重启后检查旧资源，不自动重放未批准或已撤销的 expose。

服务重建后 Guest IP 可能变化：旧 exposure 失效并关闭，或通过显式 restart/reconcile 更新 generation 后重新批准；不能只用 DNS/IP 重连到同名陌生目标。每个新连接至少重新检查目标；已建立连接的撤销延迟和关闭策略需要明确，不能声称零延迟撤销。

proxy 应限制最大连接数、空闲超时、单连接缓冲和总资源；双向流转发与 half-close 有真实测试。日志不记录 HTTP 正文、Cookie 和 URL 秘密。

### 9.6 网络验收边界

同时验证 Docker 只绑定 loopback 和 OrbStack Expose ports to LAN 已关闭。配置不可读或无法证实时，不声称 LAN 安全。不要为通过测试自动改宿主 DNS、路由、防火墙、VPN 或 OrbStack 全局设置。

从 Mac 成功 curl 127.0.0.1 只能证明本机可访问；LAN 不可访问需要另一台受控设备真实尝试并记录来源地址、目标和结果。没有该环境时明确为未执行，P4 完成报告不得把它记成通过。

.orb.local 和 Guest IP 可能绕过 localhost proxy；如果无法通过受支持机制限制，产品只能承诺“受管发布端口为 loopback”，不能承诺“所有访问只能经 Port Manager”。Guest→Host host.docker.internal 可达性另测，默认 bridge 不是阻断证明。

### 9.7 P4 验收

- 独立 sidecar WebUI 持续存活；退出 Agent 后 Mac 仍可访问；新 Agent 能查状态/日志并停止。
- 临时 dev server 可查实际进程/健康，workspace 重启标记 lost。
- 两个并发 auto port 获得不同实际监听端口；固定端口冲突不杀占用者。
- expose/close 幂等语义明确；关闭后 listener 确实释放，不误关另一个项目。
- 容器重建、IP 改变、旧代理、Host manager 重启、readiness 超时、VPN 路由异常均不误连或静默回退。
- 未授权 Compose 字段、Host mount、0.0.0.0/::、host network、未知 image、未批准拉取/构建均零副作用拒绝。
- open_url 从真实 exposure 打开 WebUI，输入不能改成任意 URL 或跨项目端口。
- 真实 harness 能启动/查询/停止允许服务；审批与强制 journal 完整。
- LAN 和旁路访问按实测结论交付，有缺项则保留明确验收阻塞，不能用 mock 代替真实网络结论。

## 10. P5：恢复、升级与并发加固

### 10.1 先做只读 reconciliation

拟新增 runtime reconcile --plan / project rebind --plan 等命令，具体语法实现时冻结。默认只比较：

- Host 期望记录、project/approval revision、容器实际 ID/StartedAt/配置。
- terminal 实例、service 容器、exposure listener/目标。
- 外置卷和 engine 数据位置。
- intent 未完成、孤儿资源、缺失资源、未知 ownership。

输出每项 desired/observed/difference、风险和建议动作。只读查询不得借机修复或自动删除。

apply 必须引用刚审核的 plan digest、原状态 revision 和期限；执行前重新比较，任何变化返回 plan_stale。不要重新计算一个不同计划后继续用旧批准。

### 10.2 项目 schema 与重绑定

目前 project schema=1 且 Load 只接受 revision/generation=1。支持 rebind 必须一起设计：

1. 新 project schema（建议 v2）和严格读写/显式迁移契约；旧二进制拒绝新 schema。
2. Revision 表示授权记录修改次数；generation 表示执行实例身份变化。仅 policy revision 变化不冒充 runtime generation。
3. CAS：expected_record_digest + expected_revision。并发重绑定最多一个成功。
4. 原记录备份/迁移 intent/完成标记；恢复读取时只有一个明确权威状态。
5. 全部旧 session、child、terminal、service、exposure 与旧 approval 失效；不得把它们引用偷偷改指新绑定。
6. 新 session 和新对象必须显式创建；容器同名或新 StartedAt 不自动认领。
7. 即使回退代码，也不把 schema v2 擦成 v1 来骗旧二进制加载。

旧 project 不必在“查看状态”时迁移。迁移是显式写操作，先 dry-run、备份和检查兼容性。

### 10.3 显式 Runtime 生命周期

start/stop/restart/provision/rebind 的策略和能力分别建模，不挂在 health 里。

- start/restart 之前再次验证外置盘、engine、approved spec，不能靠上次成功缓存。
- restart 后 StartedAt 改变，应返回需要确认/应用的新绑定计划；原 session 不自动恢复。
- stop 对精确 ID，展示受影响 terminal/service，明确进程内存会丢失。
- provision 不使用缺失 bind source 自动 mkdir；镜像、volumes、network 的创建范围和保留策略写进 plan。
- 任何 reset/prune/down -v/清空数据/迁移 OrbStack Storage 不属于一般 repair；需独立的具体授权和可核对备份。

### 10.4 故障矩阵与恢复语义

| 事件 | 必须观察 | 允许的自动行为 | 不能自动做 |
| --- | --- | --- | --- |
| 外置卷消失/身份变化 | storage_unavailable，相关请求拒绝 | 记录失效、关闭本任务自有通道 | mkdir 空挂载点、启动/重建 engine |
| Docker 暂时不可达 | unavailable，既有连接废弃 | 有界只读重新探测 | 改 context、调用 orb start 伪装查询 |
| 同 ID 容器重启 | StartedAt 变化 | 标记旧对象 lost/stale | 重放命令、重用旧 terminal |
| 同名新容器 | exact ID 不匹配 | 提供重绑定 plan | 自动重新登记 |
| 磁盘满/状态写失败 | intent/result 是否已持久化 | 明确 unknown，拒绝新副作用 | 清共享缓存/丢审计继续执行 |
| Host 睡眠/唤醒 | 连接、时间、端口、runtime 重新验证 | 重新建立经检查的新连接 | 假设旧 socket/approval 仍有效 |
| 服务/代理孤儿 | operation_id + exact identity | 报告、生成清理 plan | 只凭名字/PID杀进程 |
| OrbStack/harness 升级 | 新版本/哈希/seam probe | 不兼容即拒绝 | 沿用旧 probe verdict |
| journal/记录损坏 | schema、完整性、边界 | 保留证据，只读诊断 | 删除记录再“重新初始化成功” |

磁盘满优先用受控故障注入或隔离小容量环境，不填满用户工作盘。物理拔盘、重启全局 engine、系统睡眠/网络改变等破坏用户其他任务的实验必须单独确认操作窗口。

### 10.5 备份与还原

交付明确 runbook：

- 源码以 Git/已确认未提交内容备份；数据库使用逻辑 dump；named volume 使用一致性策略导出。
- 数据备份与控制状态分开。控制备份不得明文带模型凭据；若必须含敏感状态，说明加密和恢复权限。
- 校验归档路径穿越/symlink，禁止不可信 tar 自动向 Host 任意路径解包。
- 在新的随机容器/卷/项目 ID 中恢复，不覆盖原资源。
- 验证数据校验和/数据库内容/服务实际运行，再说明恢复成功。
- 恢复数据不等于恢复旧 daemon/container ID 或旧 approval；重新登记并批准，旧终端内存不能恢复。
- 不以单个 data.img.raw 为唯一备份，不手改 OrbStack 内部磁盘。

### 10.6 P5 完成条件

可恢复操作日志、并发 CAS、schema 升降级拒绝、所有故障矩阵、孤儿治理 dry-run/apply、真实备份还原、升级后的真实 harness conformance 均有证据。未执行的硬件/网络/平台测试逐项保留，不能合并写“恢复全部通过”。

## 11. 跨阶段验证矩阵

| 编号 | 用例 | 最低层级 | 必需阶段 |
| --- | --- | --- | --- |
| G01 | guard 失败无 Host fallback、无额外 Docker dispatch | 单元 + 原生 canary | 全阶段 |
| G02 | context/daemon/container/StartedAt/volume 变化 | 单元 + 隔离原生负例 | 全阶段受影响部分 |
| G03 | warm pool/stdin/transport 首次失败永久废弃 | race 回归 | 全阶段受影响部分 |
| W01 | 所有文件 API 根约束与两个路径操作 | 单元 + 真实 Guest | P1 |
| W02 | symlink 交换、hardlink、根删除和旧 helper | 真实 Guest 竞态/负例 | P1 |
| W03 | Codex native patch/Claude cwd 与 Host 无误写 | 真实 harness + dummy mock | P1 及入口变更 |
| A01 | 审批参数匹配、撤销/过期/并发重放 | 单元/race + bridge | P2 |
| A02 | journal 写盘失败/半写/动作后结果失败 | 故障注入 | P2 |
| A03 | bridge 固定配置与任意 Host shell 拒绝 | 真实 harness | P2 |
| T01 | REPL ≥10 轮，重启 harness 后变量保留 | 真实 Guest + harness | P3 |
| T02 | 输出首字节/cursor/限额/丢失/重复输入 | 集成 + 故障注入 | P3 |
| T03 | 容器重启旧 terminal lost、不重放 | 隔离真实容器 | P3 |
| S01 | sidecar/临时服务后台存活和精确停止 | 真实 Guest + Mac | P4 |
| N01 | loopback 实际监听/冲突/并发 auto port | 原生 Mac | P4 |
| N02 | LAN 拒绝、.orb.local/Guest IP/Guest→Host 边界 | 真实网络 | P4 |
| R01 | plan 过期/CAS/多文件中断恢复 | race + 故障注入 | P5 |
| R02 | 从独立备份恢复可用新项目 | 真实隔离环境 | P5 |
| C01 | legacy suites + make check + lint | Linux Guest | 各阶段末 |
| C02 | Darwin/Windows 交叉构建 | Linux Guest | 涉及平台/公共包 |
| C03 | 原生 macOS与真实已安装 harness | Mac 候选，Guest 编译 | managed 行为变更 |

原生 Windows 与 hosted CI 单独标记；交叉构建不能替代它们。检查失败要保留首个真实报错及根因，不能通过删测试/放宽 policy/替换真实入口让表格变绿。

阶段证据建议保存到 docs/validation/Pn.md；只在真正实施该阶段时创建。大日志放已批准外置证据目录，文档记录路径、摘要和 hash；不要把秘密或海量临时二进制提交仓库。

## 12. P0 回归清单

以下来自 P0 实际审计问题，后续 reviewer 必须按改动范围核对：

1. **参数解析一致性**：管理入口使用最终解析 session；-session、-- 和位置参数不能绕过 managed 规则。
2. **Host 路径别名**：case alias、/var ↔ /private/var、symlink、hardlink、缺失控制叶子的最近存在祖先。
3. **可信程序选择**：哈希在 --version 前；cwd/PATH/Node 注入与可执行别名不能来自 Guest 可写目录。
4. **失败锁存**：状态、FileOps、transport 内层任何首次失败都进入同一个永久废弃状态；初始化不覆盖该状态。
5. **错误分类**：活跃 daemon 上精确 container ID 缺失为 identity_changed，而非笼统 engine_unavailable。
6. **跨平台状态写**：Windows 不对只读目录句柄强制 FlushFileBuffers；macOS 合法临时根先规范化，负例保留。
7. **Claude cwd 写入**：仅允许测得的 system temp 叶子模式、独占临时文件+rename；不截断链接目标。
8. **旧 child 身份**：session down/up 同名变 Local 后旧 managed child 仍必须拒绝。
9. **harness 子命令绕过**：Claude URI/管理子命令、Codex resume/review/config/cwd override 不因新 capability 宽松开放。
10. **实际存储归属**：路径存在不代表位于批准卷，nested mount 要拒绝。
11. **损坏不是不存在**：dangling session symlink/new schema/corrupt record 不能被重建覆盖。
12. **真实 probe**：exit 0 + canary + 正确 cwd；禁止 compound shell apply_patch 冒充 native custom patch；Codex 无 legacy shim 兜底。

对新增 Host 状态目录、bridge 可执行文件、daemon socket、approval 文件和生成 Compose，重新应用第 2、3、10、11 项。不要只保护 P0 原来的固定目录列表。

## 13. 需先实验的技术问题

P1 对应实验已完成，证据见 [P1 验证记录](validation/P1.md)；其余问题在对应阶段开始时使用最小、可撤销的试验验证。技术版本变化时查对应版本官方资料和实际程序，不能只复制本手册或网上片段。

| 问题 | 最小实验 | 不成立时 |
| --- | --- | --- |
| Go 根目录 API 是否覆盖完整 P1 操作 | Linux 1.25.8 下读/写/rename/remove + symlink 竞态 | 补受审 descriptor-relative 实现，不能退回 Host realpath 预检 |
| rooted helper 能否无损支持 native patch | 真实 Codex custom patch add/update/delete/move | 修正确切 fs 调用链，不提供 Host fallback |
| cwd 根约束是否延续到实际启动 | 同一 Guest launcher 持有目录句柄进入并 exec，持续交换父目录 symlink | 修最终启动链，不采用先 realpath 后字符串 cd 的两步兜底 |
| 当前 Claude/Codex 是否支持受控 bridge 配置 | 真实 binary + mock 发起单个允许/拒绝 capability | 保持能力 unsupported，重新设计受控接缝 |
| tmux 安装版本是否支持所需输入/pipe-pane | 空参数/特殊字符/快退出/大输出/重连实验 | 收窄公开字节契约或另提 supervisor 方案 |
| tmux 日志建立是否会漏首字节 | 高频立即输出即退出进程 + 屏障前后对照 | 修启动顺序，不以 capture-pane 拼凑“无损” |
| Compose 渲染/启动是否有隐式扩展 | 使用恶意 .env/include/build/override 夹具，观察实际调用 | 收窄生成配置与固定执行环境；不能接受仓库任意文件 |
| loopback 是否真的不经其他路径暴露 | inspect + Mac 实测 + LAN 设备 + .orb.local/Guest IP | 如实限制承诺，需要系统设置变更则单独确认 |
| 多记录迁移中断能否唯一恢复 | 在每个写盘/dispatch 边界中断 | 简化权威记录或补 journal，禁止猜测最新文件 |

开发前无需泛查所有最新产品。只核查当前阶段依赖，记录明确版本，证据足够即停止扩搜。

## 14. Sol 工作指令与交接模板

### 14.1 可直接复制的启动指令

~~~text
你是 AgentReach 的实现代理，模型 GPT-6.1 Sol，reasoning xhigh。
只实施用户已确认的阶段：<Pn / 已确认子里程碑>。
仓库：/Volumes/MPM/sth/agentreach。
先读 docs/POST-P0-DEVELOPMENT-PLAN.zh-CN.md 对应章节、
docs/MANAGED-PROJECTS.md、相关代码/测试和 AGENTS 规则。

当前确认基线：<commit>。允许改动范围：<文件/包>。
目标：<具体用户行为>。本批验收：<编号和实际测试入口>。
不做：<下一阶段、安装覆盖、push、全局环境/存储变更>。

实施前返回简短调用链、核心实现逻辑、需验证假设。
已批准方案内常规细节自行解决；真实架构/授权变化先交 composer。
只在 Linux Guest 部署依赖、格式化、编译、测试。
Host 仅做控制和运行 Guest 交叉构建的原生验收候选。
记录真实容器 ID/版本/哈希，历史名字不是身份授权。

不得削弱 guard/审批/路径边界来过测试，不得修改无关用户改动。
不得自行提交；完成后给精确 diff 文件清单和证据，commit 由 Luna。
不得声称未运行的测试通过。额度不足时停止在可恢复状态并交接。
不递归派发整套工程，不启动 Pn+1。
~~~

### 14.2 composer 每批必须补齐的任务卡

~~~text
任务 ID：
用户授权来源 / 停止边界：
阶段 / 当前 commit：
输入与期望输出：
现有调用链 / 已确认根因：
核心逻辑（按顺序）：
允许修改文件：
必须保持的不变量：
数据结构 / schema / 兼容性：
错误与失败状态：
并发 / 锁 / 重试与幂等：
实际副作用与所需资源：
验证命令或用例编号：
验收通过的客观证据：
回退方式：
未决假设与实验：
~~~

不能只发“实现 P3，按蓝图做”这种任务。必须明确当批的对象状态机、入口、后端限制和证据门槛。

### 14.3 实现代理交接

~~~text
基线 / 当前工作树：
已改文件与用途：
核心行为已经做到：
未完成：
验证：命令、环境、退出码、证据路径、binary SHA：
未执行验证及原因：
曾失败的假设 / 现在的结论：
运行中的资源 / 精确 ID / 是否可清理：
最后一个真实错误与复现：
下一步最小动作：
不能重复执行的有副作用动作：
需要 composer 决定的实质变化：
~~~

中断后先查进程/文件/资源实际状态，再继续。未知是否执行成功不等于没有执行，尤其 create/start/expose/approval consume。

## 15. 验收、审计与 Luna 提交模板

### 15.1 阶段完成检查

- 用户目标的真实入口完成，不仅内部方法存在。
- 所有新数据有 schema、严格解析、权威来源和故障语义。
- 有效请求通过；越权/错误请求在副作用前失败。
- runtime/路径/审批失效后旧句柄、进程输入、代理等行为符合契约。
- 单元、集成、原生、harness、网络结果分开，必需项无未解释缺口。
- 用户文档、能力限制、准备方式、回退方式、验证记录与代码一致。
- 独立 reviewer 未参与该实现/调试；意见已核对、修复并重验。
- 文件清单冻结，工作树变更归属清楚；没有密钥、日志污染或未授权资源修改。

### 15.2 独立审计指令

~~~text
只读审计 <Pn>，基线 <base>，候选 <head / diff>。
你未参与实现与调试。不要扩展功能、不要修代码、不要做破坏性实验。
检查：目标是否真正完成；请求到副作用的完整链路；
root/身份/授权/审计/失败锁存；跨进程并发与崩溃；
真实 harness 与测试证据是否覆盖宣称；P0 回归与 legacy 兼容。
优先高影响可复现问题，给文件/函数、输入、实际与预期、
现有测试为什么没覆盖、最小修正方向。
区分 blocker、应修问题、可后置建议；没有证据不凭空提大重构。
返回：可提交 / 不可提交 / 证据不足，以及未执行部分。
~~~

### 15.3 Luna commit 指令

~~~text
仅提交已完成并审计通过的阶段 <Pn>。
仓库 <path>；预期 parent <hash>。
允许 stage 文件：<精确清单或清单文件>。
先核对 HEAD、status、diff；若出现额外变更或 parent 不符，报告而不猜测。
不得修改产品代码，不得 add -A，不得 amend、reset、clean、push。
按审阅的标题/说明提交，保留 hooks。
返回 commit hash、parent、提交文件清单、提交后 status；
如有剩余变更，指出归属，不能声称工作树干净。
~~~

提交完成后 composer 再核对 Git，向用户报告实际能力、主要验证、限制和停点。未获新阶段授权不继续。

## 16. 回退与故障处置

| 变化类别 | 回退方法 | 不应做 |
| --- | --- | --- |
| 纯代码且未变 schema | revert 本阶段 commit；运行受影响回归 | reset 丢用户改动 |
| 新 helper/bridge | 停用新入口，保留记录；按明确版本回退 | 用不支持协议的旧 helper 静默继续 |
| policy/approval | 撤销新能力授权；保留 journal | 删除审批/审计掩盖已发生副作用 |
| terminal | 精确关闭本阶段对象；提示内存不可恢复 | 重放输入“恢复现场” |
| service/exposure | 关闭精确自有代理/服务，保留数据卷 | down -v、prune、按名字删除 |
| 新 project schema | 用兼容二进制处理或恢复经审阅的控制备份并重新核对资源 | 把新 schema 字段删掉骗旧版加载 |
| 数据/环境升级 | 按备份 runbook 在独立新资源验证恢复 | 手改 OrbStack 内部 raw disk |

失败报告须回答：动作是否可能已发生、哪个对象受影响、当前状态是否可确认、什么调用仍安全、下一步可逆检查是什么。不要在未知状态自动重试副作用。

## 17. 蓝图追踪与最终交付

| 蓝图目标 | 本手册落点 | 完成证据 |
| --- | --- | --- |
| 默认 Guest、禁止静默 Host fallback | P0 不变量 + 各阶段 guard | G01–G03、真实 Host/Guest canary |
| Workspace/PathMapper | P1 | W01–W03；分清文件 API 与 Shell 边界 |
| 类型化 Host 能力/审批 | P2；open_url 在 P4 闭环 | A01–A03、真实受控 bridge |
| 持续 REPL | P3 | T01–T03、Agent 重启继续同一变量 |
| 后台 WebUI/端口 | P4 | S01、N01、N02、真实 open_url |
| Runtime 生命周期与恢复 | P4 受控 sidecar，P5 workspace 生命周期 | R01、故障矩阵 |
| 外置盘/升级/备份 | P0 guard + P5 runbook | R02、原生环境故障证据 |
| Xcode/Simulator | 明确后置 | 另行批准的能力规格；本轮不冒充已完成 |

完整交付包至少包括：

1. 每阶段独立 commit 和验收记录。
2. managed 使用说明：准备 → 登记 → 普通开发 → Host 能力 → 终端 → 服务 → 恢复。
3. 配置 schema、审批/错误契约、输出 cursor/幂等语义。
4. 镜像/helper/bridge/daemon 的受信准备与升级说明。
5. 威胁边界：Guest shell、同 UID Host、Guest 网络、OrbStack 域名、PTY、审计与撤销的实际限制。
6. 备份还原与故障 runbook，明确不自动做的破坏性动作。
7. 未执行测试、未支持功能和后置任务列表。

**当前停点**：P2 已按用户授权推送本人 fork；P3 实现、完整复验和独立复审通过，测试资源已精确归档清理，交付止于 Luna 本地阶段提交及主代理核对，不自动 push。P4/P5 仍需独立方案与授权。P3 实际接口及证据见 [TERMINALS.md](TERMINALS.md) 和 [P3 记录](validation/P3.md)，未验证项不得视作通过。
