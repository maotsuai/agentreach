# AgentReach P3 新 session 开工交接

本文件是用户要求的新项目 session 的实施交接。任务是完成主计划第 8 节的完整 P3，不止于提出方案或做出内部 API。先读本文、[主计划](POST-P0-DEVELOPMENT-PLAN.zh-CN.md)第 5、8、11–16 节、[当前 managed 契约](MANAGED-PROJECTS.md)、[P2 policy 契约](POLICY.md)及实际调用链。

## 最新授权、模型与停点

- 用户在 P2 完成后明确要求：“允许推送，注意不要推到上游去了，只是我这个 fork 的对应分支；然后在项目内创建一个新 session，把方案和必要的交接项提供给新 session 的模型，让它开始跑 p3 目标，模型使用 6.1 sol xhigh”。
- 本 session 的模型指定 **GPT-6.1 Sol / reasoning xhigh**。自身直接承担实质实现，不另开替代实现模型。用户已要求按已有 P3 方案开工，不重复索取同一阶段的初始许可；先简短说明当前调用链、核心方案及实验缺口，然后执行。范围、架构或权限实质变化、需要缩小验收契约时，说明证据后另行确认。
- 主计划和 P2 交接中的“不进入 P3”是此前停点，已由本次用户授权更新。实施范围止于 P3；不自动进入 P4/P5，不自动 push P3，不发布、不覆盖用户全局安装、不升级 OrbStack/Claude/Codex。
- 必要的有界分析可委派；重要实现验证后安排未参与实现、调试或修正分析的新代理独立审计。修复并重验后，由 Luna 按精确清单本地阶段提交，主 session 再独立核对。不要递归派发整套工程。
- 本交接不授权向其他聊天或人员发消息。新 session 直接向自身用户报告，无需向旧 session 回报。

## Git 基线与文件归属

- 仓库 `/Volumes/MPM/sth/agentreach`；分支 `codex/orbstack-execution-plane`。
- P2 commit **`ef14f25e489177e4ce4e1e203d9c4329726ab5d7`**；parent `c6d0170d00ad99524e822fc5554dc9080a7dc6bc`。P2 共 53 文件，已完整验收、独立复审通过、Luna 本地提交、主代理逐个 committed blob SHA 复核。
- 用户 fork `origin`：`https://github.com/maotsuai/agentreach.git`。`upstream`：`https://github.com/bojieli/agentreach.git`，禁止向它推送。
- P2 已按最新授权非 force 推送到 fork 的 `refs/heads/codex/orbstack-execution-plane`，没有推其他分支或 tags。于 `2026-11-06T12:07:14.589652+00:00` 独立 `ls-remote` 确认远端 SHA 等于 P2 commit。证据 `/private/tmp/agentreach-p2-push-result.json`，外置副本见下文 evidence/push-result.json。无需再次推送 P2。
- 推送前工作树干净；随后新增本文，是本任务交接产物。开工时它可能为唯一 untracked 文件，应保留并随 P3 同步，不视作未知用户改动。仍须重新检查 HEAD/status，保护后来出现的其他改动。
- 不 reset/clean、不 add -A、不 amend、不跳过 hooks、不改 Git 身份配置。阶段提交只纳入核验后的精确清单。不要为创建新 session 自动建 worktree、迁移仓库或切换分支。

## 已核对环境及存储约束

- 所有 Go fmt/vet/test/build/lint、依赖安装只在 **Linux Guest**。Host 仅做只读调查、Docker 控制、编辑源码及运行 Guest 交叉构建的原生候选；不得在 Host 安装第二套 Go 开发环境。
- 开发 Guest 完整 ID：`622e55362ab130301a112feea6499b62fff1313919502faac9775dbc4fa83b6c`。本次交接重新核对：image `sha256:c9f6a16b80584a752f2e3d06a32b0882e3e3d7bfd934ee1d2f624b560f9e1183`，StartedAt `2026-10-31T03:01:41.099068654Z`，Running=true，UID/GID `10001:10001`。
- Guest `/workspace/agentreach` bind RW，嵌套 `.git` bind RO。使用 `docker --context orbstack exec <完整ID> bash -c 'cd /workspace/agentreach && ...'`；不要 login shell。不要重启/重建该开发 Guest。
- Guest Go 1.25.8 / `GOTOOLCHAIN=local`；golangci-lint 2.12.2 在 `/home/developer/.local/bin/golangci-lint`。Go cache/module/GOPATH 均在外置 OrbStack engine，实际 data_dir `/Volumes/APAP/vm/Orbstacks`。版本与落点使用前重核。
- 本次只读运行开发 Guest 的 `/usr/bin/tmux -V` 得到 **tmux 3.3a**；没有启动或连接 tmux server。该结果不证明新 fixture 的版本，也不证明输入、启动屏障、输出收集契约成立；必须实际验证。
- 旧 P0 容器 `670140ac13d8ad3fa9b7e1696f67a0328faae646e724fea74071b3898084b799` 保持原状。不得重启它、开发 Guest、全局 engine 或用户的 tmux server。
- 已重新原生核对外置卷：MPM 为 ExFAT，UUID `C48A19AC-6C78-34CA-8969-BD408C67CAFE`；APAP 为 APFS，UUID `D1814C29-318B-418F-B5F6-1F7E324E5B42`。操作前仍须确认挂载、UUID、实际路径及所需能力；禁止写空挂载点或静默回退内置盘。
- 普通大产物放本任务独立的 `/Volumes/MPM/MCACHES/agentreach/<P3任务ID>`；需要私有权限、APFS 或稀疏文件能力的资源放 `/Volumes/APAP/MCACHES/agentreach/<P3任务ID>`。隔离 REACH_HOME、harness homes 也优先放合适的外置位置，监测实际占用，勿让缓存静默积累到内置盘。
- 本阶段为验证 P3 显式准备自有隔离项目、容器、helper/collector、任务哨兵和 named volume，以及精确清理这些资源，属于实施范围。容器重启/lost 验收只允许操作本任务新建且记录完整 ID/任务标签的 fixture；不得操作既有用户资源。不得跑旧固定名称 integration 清理脚本或 prune。
- CLI 和 Docker 操作受工具权限控制时按真实权限申请，不换路径绕过沙箱。原生 `diskutil`/DiskManagement、Docker socket 在工具 sandbox 内可能不可访问；此前提升后的只读检查正常，不把权限失败冒称为卷失效。

## P2 成果与不可回退的不变量

- `internal/policy` 的单项目 schema-1 ledger 权威保存 policy/pending/approval/operation/event；审批消费与 intent 同一次单文件原子替换，file sync、rename、directory sync 成功后才能 dispatch。不得用两次独立文件写冒充原子事务。
- 显式 Host operator `policy init/set/inspect/approve/revoke`；Guest 默认 allow、Host 默认 deny。缺失 policy 时 managed 副作用拒绝；reads 不自动初始化。`REACH_NO_AUDIT` 不能关闭 mandatory ledger。
- `Session.BeginGuest` 返回 completion `func(outcome string, cause error) error`；CLI exec、process/start/stdin 和 rooted write/mkdir/remove/rename/copy 已覆盖。P3 新增副作用必须复用该授权/审计语义，不创建可绕过的另一套准入。
- Guest 完整请求用私有 per-project key 的 HMAC，原始 shell/env/stdin/文件内容/外部错误文本不进入安全 journal。终端输出日志与安全 journal 分开；终端输出可能敏感，不能当防篡改证据。
- `outcome_unknown` 必须对外保留 operation ID 和原始原因，即使结果记录本身已保存。intent 前拒绝不能误报为已派发未知；已派发未知不能自动重放。Host MCP 错误保留 Operation；stdin 部分写首次及重试都保留同一未知失败。
- 现有预算：完整规范请求/managed 入站帧/MCP frame 64 KiB（含 JSON/base64 和绑定开销），ledger 16 MiB，pending/approval 各 256，operation 4096，event 8192；有界 lock 3 秒。不能静默截断、淘汰或清空审批/审计来继续。
- MCP 连接共享永久 runtime/identity guard 失败：包含 status、工具预检查和 ledger 锁内 check；恢复条件只允许新连接重新核验。正常 policy/approval/clock/ledger 业务拒绝不因此永久锁存。P3 terminal 对象可保存为 unavailable，新 attach 必须是新连接，不能复活失败通道。
- project schema 1、现有 revision/generation=1、session schema v2 保持。P3 用独立终端对象 schema，不假称现有 project 支持 rebind/reconcile。
- P1 路径只映射一次；下层固定 Guest namespace；严格分量嵌套拒绝、完全相等允许；rooted helper 无宽松兜底。P3 cwd/控制状态/collector 路径同样执行相应保护。普通 Guest Shell 仍不是 OS 沙箱。
- 当前 Host MCP 恰为 `host_plan/host_execute/host_status`；Claude 只承认精确生成的 bridge callback，Codex 保持 `include_local=false`、精确 binary/argv/cwd/env/binding callback 进入 typed handler，绝不执行任意 Host argv。P3 如增加 terminal 工具，必须是固定类型化 Guest 终端适配，同时同步受控配置、探测和真实启动验收；不得暴露 operator 或借机开放通用 Host executor。
- P2 生产 `open_url` 因无 P4 exposure 来源返回 `exposure_unavailable`；port/service/runtime driver 为 unsupported。P3 不顺带实现后台服务/WebUI/端口编排。

## P3 首选实施方案

范围：主计划第 8 节的 tmux 文本交互终端。terminal_id 在 Guest 容器继续运行时跨 Host CLI/Agent/harness 重启存在；普通 exec-server process 保持原语义，不自动转持久终端，不给所有 Docker exec 加 `-t`。

1. 由一个掌握终端身份、状态和锁的 Terminal Manager 负责端到端生命周期，优先独立小包；Host CLI/MCP/harness 只做类型化适配。提供 create/write/read/resize/interrupt/inspect/list/attach/close 对应能力，参数和返回格式实际冻结并写文档，不只交内部函数。
2. tmux 用固定可信可执行文件、AgentReach 专用 socket/受控配置和每终端随机名字；不加载项目 tmux.conf、不连接默认 server。绑定 project digest、container ID/StartedAt、后端实例/nonce、session/pane 身份，不能凭“同名存在”恢复或按复用 pane/PID 发送延迟请求。
3. 独立严格终端记录和有界跨进程锁管理 creating/running/exited/unavailable/lost/closing/closed/unknown 等实际状态。明确与 P2 ledger 的权威关系和写盘顺序；dispatch 前必须有 durable intent，已发生但记录失败要保留精确可发现 orphan，不盲重建或全局清理。lost/closed ID 不复用。
4. create cwd 复用 P1 的实际根约束与持有目录身份；argv 保持数组。后端必须接收 shell 字符串时，只在一个集中函数逐参数 quote，并实测空参数、特殊字符、换行和选项注入。不要 `strings.Join` 或把模型内容直接拼入 shell/send-keys。
5. 输入采用已测的 literal 或 load-buffer/paste-buffer 固定 argv 接缝；文本、控制字符、Enter/Ctrl-C、NUL/二进制/粘贴边界先实验。input_id 持久化接受/发送中/已发送/未知状态，同终端并发输入串行；重复已确认返回原结果，派发后未知不自动再发。accepted 仅承诺后端接收，不承诺应用已执行成功。
6. 输出来自 **pipe-pane 增量字节日志**，不能以 capture-pane 快照拼凑无损流。使用启动屏障：先建立 pane 与 collector，确认 ready 后才释放用户程序，实测瞬时输出即退出。推荐现有 Guest helper 的一个固定 collector 模式，负责计数、限额、错误/结束标记和 flush；不引入新的通用 PTY supervisor/Host daemon/socket 架构。
7. 日志在 Guest 专用数据位置，不依赖 Host 长期轮询继续收集；Host 仅保存身份/cursor/必要状态。每实例不可复用 epoch + 单调 byte offset，返回原字节/base64、next cursor、state、earliest cursor/明确 gap。相同 cursor 在保留范围内可重读；UTF-8 分片不改原字节；PTY stdout/stderr 通常合流，不能虚构独立来源。
8. 冻结并公开 input/read/frame/log/记录数量等硬上限及耗尽行为；不静默覆盖日志或淘汰 input_id。输出限额采用明确停止收集/关闭终端并标记 output_limit 等已审查策略；collector 故障、日志截断/丢失、旧 epoch 显式 gap/lost，不能 offset 归零冒充旧流。退出后须确认 collector 排空与最终 offset 持久化，再声称输出完整。
9. Host CLI 退出、Agent 一轮结束不关闭终端。attach 使用新 guard 且核对完整真实身份；resize 用真实 PTY 观测，interrupt 首版只承诺已测 Ctrl-C。close 精确针对本终端，返回已确认结果或 unknown，不能 kill 默认 server/容器全部进程；进程后代是否全结束要实测。必要清理不得成为越过身份保护的任意操作通道。

影响：增加受管持久终端对象、Guest tmux/collector 准备要求和受控工具面；普通执行与 unmanaged 行为保持。Guest 同 UID 可篡改 tmux/log，输出不是强认证流。容器重启丢失 REPL 内存，不承诺恢复；物理拔盘、全局 engine/网络变更、原生 Windows PTY 和 P4 服务不在本阶段。

回退：revert P3 阶段提交并停止新入口，保留项目、policy、ledger、终端身份/日志及必要证据。只精确处理本任务 terminal/fixture，不删安全审计、不降 schema、不重启开发容器或杀整个 tmux server。

## 开工实验与验收

先在隔离资源内验证 tmux 3.3a（或 fixture 实测版本）的输入/pipe-pane/collector/屏障最小链路；现有版本字符串不是可行性证明。若无损启动、限额和结束 drain 做不到，明确新证据并重新提交缩小契约或 supervisor 方案，不能边做边降标准或宣称完成。

- T01：真实 Guest REPL 至少 10 次跨调用输入输出，变量保留；完整退出并重启真实 Claude 和 Codex 后重用 terminal_id；Host CLI/bridge 重启而容器不停，输出继续收集并从 cursor 补读。
- T02：快速首行/瞬时退出、长输出、无换行、ANSI、UTF-8 分片、同 cursor 重读、非法 cursor/旧 epoch、输出限额、collector 故障、日志丢失/截断。比较实际原始字节，不靠 marker 或 capture-pane 冒称无损。
- 输入：并发串行、重复 input_id、变参拒绝、发送中/发送后崩溃未知不重放，result 写盘失败保留 operation ID；intent 失败零派发；真实 PTY resize 与 REPL Ctrl-C，未支持 signal 明确拒绝。
- T03：只停止/重启本任务随机测试容器；旧 terminal lost、同名新容器不能继承、内存不可恢复。多 terminal 隔离、精确 close、孤儿可发现与 runtime 暂时不可用亦需覆盖。
- P0/P1/P2：共享 guard 永久锁存、根约束/单次映射、所有新增控制路径的 private/link/bind 检查、mandatory intent/result、HMAC 隐私与预算不回归。Claude/Codex 普通 Bash/native patch 继续在 Guest；工具配置注入和任意 Host shell 拒绝。
- 阶段末完整 Linux Guest `make check`（vet + 全包 race/count=1）、`make lint`，必要三平台交叉构建；原生 macOS/双 workspace/真实固定 harness + localhost mock 验收，禁止付费模型调用。不要运行会花真实额度的 `make e2e`。
- 更新实际使用、API/字节/预算/未知结果/恢复/回退文档和 `docs/validation/P3.md`；保存原始失败证据，完成独立审计、必要修正和重验后，由 Luna 精确清单本地提交并独立核对。

## P2 证据与复用边界

根 `/Volumes/MPM/MCACHES/agentreach/p2-20261106-01a1ae61`；`bin` 为 Guest 产物/日志，`evidence` 为原生记录。最终源码/二进制清单 `evidence/final-commit-manifest.json` SHA `5fd3e01c18a30aec6374485534238af5390aac738852843cce4d8380eed9d650`。最终独立复审 `independent-audit-fixed.md` SHA `4c54e1b8e4ce0292fb8aac49a0ea741ba5bcfd9b0ecc6eb5d06b225a6605eff5`。`agentreach-p2-parent-verification.json` 与 `push-result.json` 记录实际提交/远端核对。

最终 Darwin reach SHA `2fde6898cf114af1a236ddf7098d1a8644eeb354c34b7e6312e07f0806ac7e7c`；Linux helper `ba38c45b4673c108b81ec21465d59749a5d5067a78a9b718283baec0e19503b0`；Darwin managed-conformance `cca3ec9ec0c148a474776bb24350e7e08286d03c6f9ee397ecdab36b6ed65c70`。仅复用未变化事实，P3 改动链路必须重新验收。

真实 harness 固定：

- Claude Code 2.1.220，`/Users/nyan/.local/lib/node_modules/@anthropic-ai/claude-code/bin/claude.exe`，SHA `8addc857f3fe64d5a0368af9ee50321b50afb4a6918ba3ef018ab84f5dbbe081`。
- Codex 0.148.0，`/Volumes/MPM/MCACHES/agentreach/host-test-tools/codex-0.148.0/codex`，SHA `b0308517b20543012fa2171aa3d46ce455a7456c4eb2a552ab9468ba4eeb1e50`。

P2 的两个 fixture 容器和专用卷已精确清理，不能复用失效项目冒充新验收。隔离控制状态原在 `/private/tmp/agentreach-p2-host-01a1ae61`，现已完整校验归档到 `/Volumes/APAP/MCACHES/agentreach/p2-20261106-01a1ae61/retained-control-state.tar`（0600；含私有 HMAC key，不输出原文）。归档保留历史绝对路径和退休绑定，仅作证据，不能当作新有效运行状态。

P2 首轮审计 B1 为 standalone/Claude MCP guard 失败未锁存；F2 为已派发未知结果外部响应丢失分类/operation ID。原始 overlay 复现、修正、原生双模式及独立复审证据均保留。P3 尤其检查锁内失败与首次响应，不仅检查最终 ledger 内容。

## 当前代码入口

- `internal/policy/{store,requests,types,outcome}.go`：严格权威 ledger、审批/intent、未知结果。
- `internal/session/{policy,managed,session}.go`：绑定、guard/控制路径、BeginGuest completion 与 rooted FileOps 接线。
- `internal/fileops/rooted.go`、`internal/workspace/`、Guest helper：单次路径映射、实际 cwd/文件根约束。
- `internal/execserver/{process,execserver,host_bridge}.go`：现有临时 process/stdin、固定 typed MCP callback、runtimeReady 生命周期；不是持久终端后端。
- `internal/host/{bridge,config,execute}.go`：固定 MCP 协议/配置与类型化 Host capability；终端业务状态应由 Terminal Manager 掌握。
- `cmd/reach/{policy,managed_harness,claude,harness,exec_cmds}.go` 与 `internal/harnessprobe/`：真实启动、严格探测、CLI/Claude callback，改工具面时同步核对。
- `test/conformance/managed/{main.go,host_bridge.py}`：真实 harness + localhost mock，新增终端流程不得用手写 RPC 成功替代。

开始后先核对真实工作树、卷、开发 Guest 和关键调用链，保留本文；随后实际执行实验和实现，持续到 P3 交付或有证据的实质阻塞。不要承诺不具备监控能力的后台继续，不通过换模型、降低验收或反复空重试掩盖缺口。
