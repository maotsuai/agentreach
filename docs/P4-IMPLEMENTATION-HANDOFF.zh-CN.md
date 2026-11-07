# AgentReach P4 新 session 开工交接

本文件是用户要求的新项目 session 的实施交接。目标是完成主计划第 9 节的完整 P4：服务、受控编排、端口和真实 WebUI，不止于设计或内部 API。先读本文、[主计划](POST-P0-DEVELOPMENT-PLAN.zh-CN.md)第 4–5、9、11–16 节、[managed 契约](MANAGED-PROJECTS.md)、[policy 契约](POLICY.md)、[终端契约](TERMINALS.md)及 [P3 验证记录](validation/P3.md)，再追实际调用链。历史交接中的阶段停点以本次用户授权更新，不把历史拟议接口当作已实现。

## 最新授权、模型与停点

- 用户在 P3 完成后明确要求：“推送，注意不要推到上游去了，注意只是我这个 fork 的对应分支，然后在项目内创建一个新 session，把方案和必要的交接项提供给新 session 的模型，让它开始跑 p4 目标，模型使用 6.1 sol xhigh”。
- 新 session 使用 **GPT-6.1 Sol / reasoning xhigh**，自身直接承担实质实现，不另开替代实现模型。按已有 P4 方案开工，不重复请求同一阶段的初始许可；先简短说明现有链路、首选方案及需实测的缺口，然后持续执行。范围、架构或权限实质变化、缩小验收契约时，再说明证据并确认。
- 本次授权止于完整 P4 和本地阶段提交；不自动进入 P5、不自动 push P4、不覆盖全局安装、不升级 OrbStack/Claude/Codex、不改全局网络设置。
- 必要的有界分析可以委派；重要实现验证后，由未参与实现、调试和修正分析的新代理独立审计。修复并重验后，由 Luna 按冻结的精确清单本地提交，主 session 独立核对 commit、parent、committed blob 与工作树。不要递归派发整套工程。
- 不向其他聊天或人员发消息；新 session 直接向用户报告，无需向旧 session 回报。旧 session 创建交接后不与新 session 并行改仓库。

## Git 基线与推送事实

- 仓库 `/Volumes/MPM/sth/agentreach`，分支 `codex/orbstack-execution-plane`，不要自动建 worktree、迁移、切分支或重写历史。
- P3 commit **`e40092e18d4fc999952454425a170d30b86fd15b`**，唯一 parent `ef14f25e489177e4ce4e1e203d9c4329726ab5d7`。36 文件已逐个核对 committed blob SHA256 与冻结候选一致，提交后工作树干净，独立复审“可提交”。
- 用户 fork `origin` 为 `https://github.com/maotsuai/agentreach.git`；`upstream` 为 `https://github.com/bojieli/agentreach.git`，禁止推送上游。
- 已通过 GitHub 元数据核对 `maotsuai/agentreach` 为 fork，parent `bojieli/agentreach`。P3 已非 force 推送到 fork 的 `refs/heads/codex/orbstack-execution-plane`；没有推其他分支或 tags。于 `2026-11-06T14:44:44.003417+00:00` 独立 `ls-remote` 确认目标 SHA 等于 P3。
- 推送证据 `/Volumes/MPM/MCACHES/agentreach/p3-20261106-01a1b0c4/audit-fixed/evidence/push-result.json`，SHA256 `5281d2e7e87a6fcea143754a60764402eb7b6a0be4b62f296d29c38f0e5711b8`。无需再次推送 P3。
- 本文随后新增，是本次交接产物；开工时应为唯一 untracked 文件，保留并随 P4 同步。仍须实际核对 HEAD/status，保护后来出现的改动。不 reset/clean、不 add -A、不 amend、不跳过 hooks、不改 Git 身份。

## 环境、资源与存储

- 所有 Go fmt/vet/test/build/lint、依赖安装只在既有 **Linux Guest**。Host 仅编辑/只读调查、受控 Docker 操作及运行 Guest 交叉构建的原生候选，不能安装第二套 Host Go 环境。
- 开发 Guest 完整 ID `622e55362ab130301a112feea6499b62fff1313919502faac9775dbc4fa83b6c`，image `sha256:c9f6a16b80584a752f2e3d06a32b0882e3e3d7bfd934ee1d2f624b560f9e1183`，P3 清理记录为 StartedAt `2026-10-31T03:01:41.099068654Z`、Running=true、UID/GID `10001:10001`。使用前重新核对实际身份，不能凭名称操作。
- Guest `/workspace/agentreach` 源码 bind RW，嵌套 `.git` RO。命令使用 `docker --context orbstack exec <完整ID> bash -c 'cd /workspace/agentreach && ...'`，不使用 login shell。不得重启/重建开发 Guest。
- P3 验证版本：Go 1.25.8、`GOTOOLCHAIN=local`、golangci-lint 2.12.2（`/home/developer/.local/bin/golangci-lint`）、tmux 3.3a。Go cache/module/GOPATH 均在外置 engine；实际 OrbStack data_dir `/Volumes/APAP/vm/Orbstacks`。版本与落点重新核对。
- MPM 为 ExFAT、UUID `C48A19AC-6C78-34CA-8969-BD408C67CAFE`；APAP 为 APFS、UUID `D1814C29-318B-418F-B5F6-1F7E324E5B42`。操作前原生核对卷挂载、UUID、实际路径与能力；禁止写空挂载点或静默回退内置盘。
- 普通大产物放 `/Volumes/MPM/MCACHES/agentreach/<P4任务ID>`；需要私有权限、Unix socket、APFS 或稀疏能力的资源放 `/Volumes/APAP/MCACHES/agentreach/<P4任务ID>`。隔离 REACH_HOME/harness homes/Compose 控制目录使用合适的外置位置。不要将私有 key、凭据或完整 Host env 写入仓库/普通证据。
- 为实施和验收 P4，可以创建本任务独占、记录精确身份的隔离 workspace、网络、sidecar、卷、代理和 helper，按明确安全规格审批并实际验证，再精确清理。不能把开工授权转成产品中的永久通用 Host allow；生产审批与 mandatory journal 仍须完整。
- 不操作用户既有资源或旧 P0 容器 `670140ac13d8ad3fa9b7e1696f67a0328faae646e724fea74071b3898084b799`；不得重启 engine、删除共享缓存、运行旧固定名称集成清理或 prune。缺镜像时先报告 `image_unavailable`；pull/build/provision 不是 ensure_ready 的隐含副作用。
- Docker socket/DiskManagement 可能受工具 sandbox 限制，需要时按真实权限提升，不换路径绕过，不把权限失败冒充卷失效。

## 当前实现与不可回退不变量

- P0 workspace binding 是固定快照；project schema 1、revision/generation=1、session schema v2 保持。P4 服务/端口用独立严格对象和清晰权威关系，不能顺带实现 P5 rebind/migration。为新隔离 fixture 选择项目专用网络应在创建时固定，不能临时 network connect 原 workspace 绕过 guard。
- P1 FileOps/cwd 严格根约束、单次 namespace 映射、rooted helper、控制路径 private/link 检查保持；普通 Guest shell 不是 OS 沙箱。
- P2 `internal/policy` 单项目 schema-1 ledger 权威管理 policy/pending/approval/operation/event，审批消费与 durable intent 同一次原子替换；mandatory journal 不受 `REACH_NO_AUDIT` 关闭。Host 默认 deny，operator 审批不能暴露成 Agent 通用工具。所有新副作用必须有恰当的 Guest/Host 分类、固定类型请求、审批/intent/result 与 operation ID。
- HMAC 隐私、预算、有界锁和 fail-closed 存储保持；原始 stdin/argv/env/HTTP body/Cookie/URL secrets 不进入安全 journal。结果写失败或派发不确定返回 `outcome_unknown` 和原 operation ID，不自动重放或声称动作未发生。
- MCP 的 runtime/identity guard 失败对该连接永久锁存，包括锁内检查、Host status 和后端身份错配；恢复只能新连接重新核验。正常 policy/ledger/readiness 业务错误不得全部误判为永久身份失败。
- 当前 MCP 包含固定 `host_plan/host_execute/host_status` 和九个固定 terminal 工具。Claude 精确 callback，Codex `include_local=false`，受控 binary/argv/cwd/env/binding；不开放任意 Host shell、argv、Docker CLI、operator 或任意 connect。新增工具必须同步受控配置、探测、文档和真实 harness 启动验收。
- P3 由 `internal/terminal`、`internal/terminalguest`、`internal/session/terminal.go` 掌握 tmux 身份/状态/去重/日志；专用 socket、随机 nonce、完整 runtime/server/pane 身份，lost/closed 不复活。输出来自带启动屏障的 pipe-pane 原字节日志，complete 需要自然退出证据和 collector EOF/fsync/final offset。8 MiB 日志、16 KiB input/read、64 KiB frame、64 terminal/project、256 input/terminal 等实际预算见 TERMINALS.md。
- 不把用户 terminal 一概当 service；可以复用 P3 有效组件，但 service 需自己的非交互生命周期、readiness 和精确身份。P3 close 实测 detached 后代可存活，不保证全后代终止；容器重启旧 terminal lost，不恢复内存。
- `internal/host/execute.go` 已有可信 `Executor.Resolve` / `Exposure` 接缝和 `open_url` 的 binding/id/generation/healthy/loopback 检查，目前生产 registry=nil，`open_url` 返回 exposure_unavailable；其余 service/ports/runtime driver 尚 unsupported。P4 要打通真实 registry、审批和 driver，不能用测试 Resolve 假冒生产实现。

## P4 实施方案与影响

实施完整主计划第 9 节，按 A→F 顺序，不跳过动态代理、临时服务或真实 WebUI：

1. **P4-A：模型与准入。** 新增 `internal/service`、`internal/ports`，冻结 ServiceSpec/ExposureSpec、状态机、请求预算、身份、操作幂等/未知和审批计划；在 runtime/docker 提供最小受限 mutation guard。ServiceSpec 包含 schema/project/driver、解析后的不可变 image ID/digest、argv/cwd、非敏感 env、精确 mounts/network、非 root UID、资源限额、healthcheck、manual/no restart、日志限额及批准的 guest ports。running 不等于 ready；start/restart 的 operation/spec digest 固定。
2. **P4-B：受限 Compose sidecar。** 只在 Host 私有目录由批准后的类型化规格生成最小 Compose；可信 CLI/plugin 执行链、固定 project/file/cwd/env，不能加载仓库 `.env`、override/include/extends/profiles/build。展开 image 的 ENTRYPOINT/USER/VOLUME/healthcheck 等实际行为，检查最终渲染摘要。create 不 start → inspect 实际 image/mount/HostConfig/labels/network/ports → start → readiness；配置不符保持未启动。只操作精确 ID，不运行整个 workspace compose up/down，不拉 dependencies、不 down -v。
3. **P4-C：固定 loopback 发布与 open_url。** 创建时明确 127.0.0.1:port:guest_port，核对全部实际 PortBindings。真实 exposure 绑定 service/generation/container/endpoint，可信 Resolve 才能提供 origin；open_url 只接受该 exposure 的限定相对 path，保持审批/二次核验及跨项目/任意 URL 拒绝。
4. **P4-D：workspace 临时服务。** 独立 ServiceDriver 监管实际进程、日志和限定 TCP/HTTP readiness，不使用 `docker exec ... &`/nohup 冒充完整监管；不暴露交互 write。容器/进程/绑定失效如实 lost，不凭旧 PID restart/kill。probe 不是任意 SSRF。
5. **P4-E：长寿命动态 loopback 代理。** 可新增按需启动的最小 reach-hostd，只在需要代理时运行，私有 socket/权限/实例 nonce，每请求核对项目/审批/资源；不凭 PID 文件 kill，不做 Host shell 或任意 TCP relay。`net.Listen(127.0.0.1:0)` 原子分配并持有同一 listener；IPv4 loopback 首版，禁止 0.0.0.0/::。限制连接数/空闲超时/缓冲/总资源，真实双向及 half-close 测试。明确撤销对已建立连接的关闭策略和延迟。
6. **P4-F：完整验收、文档、审计与本地提交。** CLI/MCP/harness 真实服务入口、持续 WebUI、Agent 重启、故障/网络边界、P0–P3 回归都闭环；更新实际使用文档、主计划状态和 `docs/validation/P4.md`，保留原失败证据，不降低标准。独立审计后由 Luna 精确提交，主 session 复核。

新增 Host manager、Docker mutation、服务/端口状态及工具面是已有 P4 方案范围；普通 Bash 不因此常驻 daemon。Host secrets 默认不支持，不能注入整个 Host env/env_file。服务 sidecar 必须非 root、cap_drop ALL、no-new-privileges、无 privileged/devices/Host namespace，只使用精确批准 mounts/network，不能为它放松 workspace Guard。

先实测本机 Compose 版本、可信路径与 no-build/no-deps/pull 控制、create-before-start 的最终配置、现有网络及 Guest endpoint 可达性，不凭记忆猜旗标。发现实际架构做不到契约时，提交具体证据和变更方案，不边实现边静默缩小范围。

回退：停用 P4 新入口并 revert 阶段提交；按精确身份关闭本任务 listener/manager/service，保留项目、policy/ledger、对象状态、用户数据和必要证据。不删审计、不降 schema、不重启 engine、不杀其他项目服务、不批量删 volume/network。

## 验收与明确边界

- **S01/N01/N02 及主计划 9.7**：独立 sidecar WebUI 在 Agent 完整退出后仍可从 Mac 访问；新 Agent 能查真实状态/日志并停止。临时服务实际进程/readiness 可查，workspace 重启标记 lost。
- 并发 auto port 获不同实际 listener；固定冲突不杀占用者；close 后实际释放；重复 expose/close、unknown 重试、跨项目拒绝、旧实例/同名替代/容器重建/IP 改变、manager 崩溃/重启及孤儿检查不得误连或自动重放已撤销批准。
- Host manager listener/控制路径/nonce、已建连接撤销、双向流/half-close、readiness 超时、目标重核、实际 VPN 路由异常的行为按证据验证；不能自动改宿主 DNS/路由/防火墙/VPN/OrbStack 全局设置制造通过。
- 未授权 Compose 字段、Host 控制/socket/凭据 mount、0.0.0.0/::、host network、未知 image、未批准 pull/build、输入注入、篡改渲染文件/配置/endpoint 均零副作用拒绝；意图/结果写失败与未知不能丢 operation ID。
- 同时检查 Docker loopback 和 OrbStack Expose ports to LAN 实际设置；若不可读或无独立受控 LAN 设备，不宣称 LAN 安全。Mac 本机 curl 不能替代 LAN 负例。`.orb.local`/Guest IP 可能绕过 proxy，记录 allowed/unsupported/unknown 的实际结果；无法限制时只承诺受管发布端口 loopback，不能承诺所有访问经过 Port Manager。Guest→Host host.docker.internal 单独测。
- 网络/设备/权限缺口如实保留，不伪造、不以 mock 代替真实网络；继续完成不依赖缺口的已授权工作，再报告剩余验收阻塞，不把部分完成写成完整 P4 通过。
- 真实固定 Claude/Codex + localhost 模型 mock 验收启动/查询/停止允许服务、审批/journal、open_url 真实 exposure；普通 Guest Bash/native patch/持久 terminal 不回归。不调用付费模型，不运行会花真实额度的 make e2e。
- 完整 Linux Guest make check（vet + 全包 race/count=1）、make lint，必要 Linux/Darwin/Windows 交叉构建；原生 macOS/双 workspace/真实 harness 分层记录，交叉构建不代表原生 Windows 验收。
- 只清理本任务创建且确认无占用的精确资源；先归档证据/必要数据，保留完整 ID/label/nonce 和清理结果。不能为失败无限换 fixture、增加重试或改低验收标准。

## P3 证据与复用边界

根 `/Volumes/MPM/MCACHES/agentreach/p3-20261106-01a1b0c4`；最终修正验收在 `audit-fixed`，二进制/检查在 `bin`，原生/审计/清单在 `evidence`。私有状态保留在 `/Volumes/APAP/MCACHES/agentreach/p3-20261106-01a1b0c4/{control,audit-fixed-control}`，含 HMAC key，禁止输出或复制进 Git。

- 最终独立复审 `audit-fixed/evidence/independent-audit-fixed.md` SHA256 `0b4656893de74086063e057f76bffe8ea85a7eecffb65da871e604d91405023c`。独立原样四个 overlay + 五包 race 通过，核对全部源文件/候选及真实原生/harness 证据；审计并未独立再跑全部原生矩阵，细节见报告。
- `final-commit-manifest.json` SHA256 `a7405b13b039e4ff4c2af39bc49727aa396bb223ad4194d7d53bd715faf52843`；`parent-verification.json` SHA256 `2077b628a9eb0deee0167e32090ebfb473ac08a4a4dc88f6e17427f25c607a81`，均在上述修正 evidence。仅复用未变化事实，P4 修改链路重新验收。
- P3 最终 Darwin reach SHA256 `76ef5162effc932e307326a2e7db9849b1ed5eb67edfdbd58450e5ee733a144f`；Linux helper `eb3cabaa53cc3ea44efba922d1865840a64cac5d1e494150feb86f6b12c6be86`；Darwin managed-conformance `69e2bb4f58e4123e6f82204808604ce62c6412cc13c2ef47a7c08a3503d304e1`。
- 两轮 P3 fixture 原容器已按 T03 退役，4 个同名替代容器和 2 个专用卷均已归档清理；不要复用旧失效项目。开发 Guest 10 个本任务实验目录也已核对归档清理；候选、审计 overlay、私有控制状态与测试工作目录作为证据保留，不是 P4 有效运行状态。
- 首轮审计 B1 为 lost 观察未持久化导致恢复日志后旧 ID 复活；B2 为 backend identity 失败未锁存整 MCP；B3 为终态早退遮蔽已存 input/operation；F1 为 lost 原因未保留。全部修正并原样复跑。P4 特别检查锁内失败、首次未知响应、终态持久化、恢复后同名陌生对象和重复 operation 的原结果，而不只看最终 ledger。

真实 harness 使用前核对，不更换或升级：Claude Code 2.1.220，`/Users/nyan/.local/lib/node_modules/@anthropic-ai/claude-code/bin/claude.exe`，SHA256 `8addc857f3fe64d5a0368af9ee50321b50afb4a6918ba3ef018ab84f5dbbe081`；Codex 0.148.0，`/Volumes/MPM/MCACHES/agentreach/host-test-tools/codex-0.148.0/codex`，SHA256 `b0308517b20543012fa2171aa3d46ce455a7456c4eb2a552ab9468ba4eeb1e50`。Claude MCP 发现异步，保留基于真实 tool_result ID 的等待；不能重复已执行的有副作用输入。

## 需要追踪的实际入口

- `internal/runtime/{docker,orbstack}`、`internal/project/`：固定 binding、实际 inspect、受限 mutation 入口与准备边界。
- `internal/host/{execute,bridge,config}.go`、`internal/policy/`：可信 exposure Resolve、Host 审批/intent、固定 MCP、严格请求与未知结果。
- `internal/session/{managed,policy,terminal}.go`、`internal/terminal/`、`internal/terminalguest/`：共享 guard/控制路径及可复用 Guest 生命周期组件。
- `cmd/reach/{project,policy,terminal,managed_harness,claude,harness}.go`、`internal/execserver/host_bridge.go`、`internal/harnessprobe/`：实际 CLI、受控配置与真实启动接缝。
- `test/conformance/managed/` 中 main.go、host_bridge.py、terminal_harness.py、terminal_native.py、terminal_lifecycle.py：可复用真实 binary + localhost mock 方法，不能用手写成功 RPC 冒充真实 harness。

开工先核对真实 HEAD/status、卷、开发 Guest、Compose 执行链和关键调用链，保留本文，然后执行有界实验、实现和验收，持续到完整 P4 交付或有证据的实质阻塞。不承诺没有监控能力的后台继续，不以新增 session/代理、降标准或空重试掩盖缺口。
