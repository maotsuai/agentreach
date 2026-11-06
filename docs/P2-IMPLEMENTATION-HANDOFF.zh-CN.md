# AgentReach P2 新 session 开工交接

本文件是用户要求的新项目 session 的实施交接；先读本文，再读开发计划第 5、7、11–16 节及当前代码。当前任务是实际完成 P2，不止于提出方案。

## 用户授权、模型和停点

- 用户先要求“使用 luna 推送后审查 p2 目标，审查可实现后开工”，随后明确：“允许推送，注意不要推到上游去了，注意只是我这个 fork 的对应分支；然后在项目内创建一个新 session，把方案和必要的交接项提供给新 session 的模型，让它开始跑 p2 目标，模型使用 6.1 sol xhigh”。
- P2 可行性审查及真实 Claude/Codex 固定 MCP 最小实验已完成。用户对“审查可实现后开工”的条件已满足；本方案内常规实现无需再次确认。实质越界、不可逆操作或新增重大风险仍须明确说明并另获确认。
- 新 session 指定 GPT-6.1 Sol、reasoning xhigh，自身直接承担实质代码/配置实现，不另开替代实现模型。任务内必要的有界分析可委派；验证后安排未参与实现、调试或修正分析的新代理独立审计，阶段提交交给 Luna。不要递归派发整套流程。
- 仅完成 P2。不要自动进入 P3，不自动 push P2，不发布、不安装到用户全局环境、不升级 OrbStack/Claude/Codex。
- 审查方案、主计划与代码如有冲突，先核对真实源码和证据；不得为表面通过而降低验收，不能把尚未实验的设计当既成能力。

## Git 与交接文件归属

- 仓库：`/Volumes/MPM/sth/agentreach`，当前分支 `codex/orbstack-execution-plane`。
- P1 阶段 commit：`c6d0170d00ad99524e822fc5554dc9080a7dc6bc`；parent `3ad0084cd1cefee995054f286396c4877a52a006`。
- 用户 fork `origin`：`https://github.com/maotsuai/agentreach.git`；只准推送这个 fork 的对应分支。`upstream` 是 `https://github.com/bojieli/agentreach.git`，禁止向它推送。
- Luna 已完成 P1 普通推送，主代理于 `2026-11-06T01:00:43.742625+00:00` 通过独立 `ls-remote` 核对 `refs/heads/codex/orbstack-execution-plane` 的远端 SHA 为上述 P1 commit。证据 `/private/tmp/agentreach-p1-push-result.json`。无需再次推送 P1。
- 推送核对时工作树干净。随后本交接文件作为本任务产物新增，可能显示 untracked；属于此次交接，不要当作用户未知修改丢弃。开始前重新核对真实 status/HEAD，保护任何新出现的其他改动。
- 不 reset/clean，不 add -A，不 amend，不跳过 hooks。阶段完成并独立审计后由 Luna 按精确清单提交，主 session 核对 parent、提交文件和工作树。

## 必须继续遵守的本机工作规则

- 默认简体中文，结论和证据准确，保持约每分钟有实质进展说明。失败/中断先核对真实运行状态和已有写入，再基于新增证据有界重试；不通过换代理或加预算掩盖失败。
- 所有 Go fmt/vet/test/build/lint 和开发依赖安装只在 Linux Guest。Host 仅只读调查、Docker 控制和运行 Guest 交叉构建的 macOS 候选。不得在 Host 建第二套 Go 开发依赖。
- 已有仓库不迁移；先核对外置卷已挂载及原生 UUID，禁止向空挂载点或内置盘静默回退。普通开发产物放 `/Volumes/MPM/MCACHES/agentreach`，需 APFS 的资源放 `/Volumes/APAP/MCACHES/agentreach`；OrbStack 实际 data_dir 需读真实配置，当前为 `/Volumes/APAP/vm/Orbstacks`。
- 复用开发 Guest 的完整 ID：`622e55362ab130301a112feea6499b62fff1313919502faac9775dbc4fa83b6c`。最近核对 image `sha256:c9f6a16b80584a752f2e3d06a32b0882e3e3d7bfd934ee1d2f624b560f9e1183`、StartedAt `2026-10-31T03:01:41.099068654Z`、UID/GID `10001:10001`；使用前仍须 inspect。
- Guest 源码在 `/workspace/agentreach`，仓库 bind RW、嵌套 `.git` bind RO。使用 `docker --context orbstack exec <完整ID> bash -c 'cd /workspace/agentreach && ...'`，不要 login shell。不要重启/重建开发容器。
- Go 1.25.8、`GOTOOLCHAIN=local`，golangci-lint 2.12.2 在 `/home/developer/.local/bin/golangci-lint`。GOCACHE `/home/developer/.cache/go-build`、GOPATH `/home/developer/go`、GOMODCACHE `/home/developer/go/pkg/mod` 均在外置 engine；lint 可能需显式加 PATH。
- 旧 P0 容器 `670140ac13d8ad3fa9b7e1696f67a0328faae646e724fea74071b3898084b799` 保持原状，不与开发容器、全局 engine 或用户资源一起重启/清理。
- P1 的六个临时测试容器和一个 named volume 已全部精确清理，不能复用其失效项目记录冒充有效 fixture。P2 如需真实资源，创建独立随机 ID/任务标签的 fixture 和显式新项目，保留证据；只按精确 ID+标签清理本任务资源。不得运行旧固定名 integration 清理脚本或 prune。
- P2 内显式准备新 helper、隔离测试状态/容器、任务哨兵、故障注入与自有资源清理在已授权实施范围内；不得修改旧容器绑定、全局 Docker/网络/存储配置或用户安装。物理拔盘、全局 engine 重启、LAN 配置不在范围。
- 真实 harness 验收仅 localhost mock + dummy credentials，禁止付费模型调用，不读取/输出真实密钥、auth.json、完整环境。不要把 mock RPC 成功当真实 harness 接缝成功。
- 证据足够后停止扩搜/重复测试；修改后按受影响路径验证，阶段末运行必需完整检查。代码、测试判定、用户文档和实际行为一致，明确未执行的原生 Windows/CI 等，不冒充通过。
- 本交接不授予向其他用户/服务发送消息的权限。新 session 不需要跨会话向旧 session 发回消息，可直接在自身对用户报告；用户若后续明确要求再发送。

## P1 的实际边界与已修问题

- 文档入口：`docs/MANAGED-PROJECTS.md`、`docs/validation/P1.md`、`docs/POST-P0-DEVELOPMENT-PLAN.zh-CN.md`。主计划目前仍保留 P1 阶段结束时的授权/停点文字；本次用户后续授权是 P2 开工依据，实施时同步更新所有相关文档，避免又停下来问同一阶段许可。
- managed FileOps 必须使用显式准备的 Guest `/usr/local/bin/reach-helper` rooted capability，无 Host 构建、自动上传或 POSIX/Python 兜底。FileOps 用 os.Root；启动 cwd 用持有目录 fd 的一次性 launcher。普通 Guest Shell 运行中仍有普通 Guest 文件/网络访问能力，不能宣传为 OS 沙箱。
- 已修 B1：exec-server 协议入口只映射 URI 一次，下层 FileOps/pool/reopen/派生路径/handles 固定 Guest namespace，CLI 原始路径用 Protocol。不要恢复重复映射。
- 用户已明确批准 Host 协议别名与 Guest 根严格双向分量嵌套返回 path_denied；完全相等、分量兄弟允许。包括登记 shared host_path 和可信 harness cwd 的原始/规范别名。拒绝在相关派发前；旧记录可 Load/inspect/health，无 schema 改动、迁移或 rebind。
- project schema 1、revision/generation=1、session schema v2 和 P0 guard 失败锁存保持。恢复哨兵后旧连接/handles/stdin 仍不可复活；Close/必要清理始终可用。
- managed cwd 拒绝 CR/LF、保留尾空格；物理 cwd base64 回传后验证才持久化。普通 FileOps 换行文件名仍允许。多 hardlink 普通文件内容访问默认拒绝；copy symlink unsupported。所有新控制状态路径继续执行 P0 hostpath 与 writable-bind 检查。

## 证据与候选

P1 外置证据根：`/Volumes/MPM/MCACHES/agentreach/p1-20261105-c136efc9e4/evidence`。完整 check/race、lint、跨平台、真实双 workspace、native patch、cwd 竞态、guard 失效、严格嵌套与相等 URI 复用的证据及原失败记录均在其中。最终独立报告 `final-audit-report.md`，最终提交清单 `final-commit-manifest.json`，清理 `cleanup-results.json`。

P1 交付候选在相邻 `bin`：Darwin reach SHA `b132718da6a29b294cc3fc0273663f96f2fa9e3326db868685b36ef9a9380bf1`；Linux helper `284bb010ced5cd1b33de93f9bd636476048d7302c2ef9ad3c9d09b48197a6430`；Darwin managed-conformance `d44a6e517f9a98af3234b0fd3180d56df1f42682ddfcd5f621eb53b00259773b`。仅复用仍有效证据，P2 改变的链路必须重新验收，不能冒称 P1 日志来自 P2。

真实 harness 固定版本及路径（不能悄悄升级）：

- Claude Code 2.1.220：`/Users/nyan/.local/lib/node_modules/@anthropic-ai/claude-code/bin/claude.exe`，SHA `8addc857f3fe64d5a0368af9ee50321b50afb4a6918ba3ef018ab84f5dbbe081`。
- Codex 0.148.0：`/Volumes/MPM/MCACHES/agentreach/host-test-tools/codex-0.148.0/codex`，SHA `b0308517b20543012fa2171aa3d46ce455a7456c4eb2a552ab9468ba4eeb1e50`。这是待测产品 harness；它与新 session 自身 GPT-6.1 Sol 模型是两件事。

## 新 session 的第一批动作与交付

1. 重新读本文件、主计划及当前调用链，核对 HEAD/status、外置卷、开发 Guest 和已归档证据。核实授权已满足，不再重复索取 P2 开工确认。
2. 简短报告本阶段范围后开始实现；先闭环 policy/journal 原子提交、原生时钟与副作用前拒绝，再接入全部 managed 入口及固定 MCP。发现方案实质变化先明确说明、保留已完成工作。
3. Go 操作只在 Guest。P2 需要的原生 ClockGettime 最小编译与 macOS 运行、隔离权限/短写/fsync/崩溃等实验在实施时实际执行并记录，不能沿用本审查的未验证项当通过。
4. 真实 Claude/Codex 工具桥需增加允许/拒绝及注入其他 Host 工具的负例，并重验普通 Bash/fs/native patch 仍进 Guest；MCP 不得暴露 approve/revoke 或任意 argv。
5. 更新所有相关文档、完整必要检查、全新独立审计、修复和重验后，Luna 精确清单本地提交。最终报告真实完成状态、阶段 commit、证据和剩余边界；不自动进入下一阶段或推送 P2。

以下是已审查的具体方案，实施细节可按真实证据在本范围内调整；涉及架构/权限/行为边界的实质变化仍须重新说明并确认。

---

# 已审查的 P2 实施方案

2026-11-06。基线 c6d0170d00ad99524e822fc5554dc9080a7dc6bc，分支 codex/orbstack-execution-plane。审查时仅完成只读调查与隔离临时实验，没有 P2 产品实现。

结论：P2 技术上可实现。此前 P1 推送因目的地披露授权被自动审批拒绝；用户已明确补充授权，Luna 已成功推送并由主代理核对远端。此阻塞已解除，不要再次索取同一授权。

## 已核对事实

- 现有 state.Write 仅承诺单文件 temp/file sync/rename/directory sync；不能把审批和 intent 的两次独立写盘当事务。state.Lock 可作有界跨进程串行化，stale lock 不抢占。
- 旧 internal/audit 可关闭且会跳过损坏尾部，不可作为 P2 安全 journal；保留给旧操作日志。
- 真实 Claude Code 2.1.220 的 strict-mcp-config 固定 stdio server 已完成 tools/call(host_plan) 到离线模型返回唯一 marker 的往返，exit 0。证据：/private/tmp/agentreach-p2-mcp-seam-_xx9i0ke/claude。
- 真实 Codex 0.148.0 也已完成同样往返，exit 0。它默认先通过客户端 tool_search 发现 MCP；首次 flat-tool mock 没有调用工具的失败记录保留，已按实际目录和协议修正实验。成功证据：/private/tmp/agentreach-p2-mcp-seam-1yyn_c0l/codex；中间 schema 证据：/private/tmp/agentreach-p2-mcp-seam-mvkhl3ae/codex。临时实验源：/private/tmp/agentreach-p2-mcp-feasibility.py。实验没有实现产品 policy、产生 Host 能力副作用或访问付费模型。
- Codex 隔离 mcp get 实际接受固定 command/args/cwd/enabled_tools；未知或新版本字段不能仅凭官网认定适用于 0.148。证据：/private/tmp/agentreach-p2-review-mcynynck。
- 开发 Guest 622e55362ab130301a112feea6499b62fff1313919502faac9775dbc4fa83b6c 已重核身份、StartedAt、源码 bind 和只读 .git；Go 1.25.8，Go 缓存继续位于外置 OrbStack engine。没有安装 Host Go 依赖。
- macOS kern.bootsessionuuid 在工具 sandbox 中受限，提升后的只读原生调用返回合法 UUID；没有输出具体值或修改系统时钟。Apple SDK 提供公开 clock_gettime。Darwin 跨进程 ClockGettime 编译与原生 Go 运行仍待实施阶段验证。

## 冻结的首选方案

1. 每项目一个独立 schema 的权威安全 ledger，保存 policy revision、pending/approval、operation 和完整有序安全事件；同一原子提交消费审批并落盘 intent。dispatch 在持久化成功后进行；result 失败报告 outcome_unknown，未知 intent 不自动重放。project schema 1/session v2 不变。
2. 显式 operator policy init，Guest 默认 allow、Host 默认 deny。缺失 policy 时 managed 副作用明确 policy_unavailable；只读 Load/inspect 不自动创建状态或授予权限。
3. journal 覆盖 CLI/Claude exec、Codex process/start 和 stdin 写，以及所有 managed 文件变更/copy/native patch。只读 guard、文件读取和固定能力握手不递归产生用户执行 intent；P0 失败锁存和 Close/终止清理继续有效。REACH_NO_AUDIT 不能关闭安全路径。
4. 固定 stdio MCP 只暴露 plan/execute/status。approve/revoke 仅有独立 Host operator 入口，不给模型使用。规范请求摘要覆盖完整绑定、policy revision、目标 generation、参数、资源范围及副作用。未知字段、能力和任意 Host shell 拒绝。
5. 默认审批 TTL 5 分钟，最大 15 分钟，一次使用。Darwin 使用 CLOCK_MONOTONIC_RAW + boot-session UUID；Linux 使用 CLOCK_BOOTTIME + boot ID。同 boot 跨进程可继续，boot 改变/时钟不可用/观察到 tick 或发行/保存高水位倒退则拒绝。墙钟不决定期限，不能宣称检测每一次微小调整。使用已核对兼容 Go 1.25.8 的 x/sys v0.35.0 小依赖，避免私有 runtime API 或常驻 broker。
6. 请求上限 64 KiB，ledger 16 MiB，pending 与 approval 各 256，operation 4096，事件 8192；任一预算耗尽明确拒绝，不静默截断/轮转。读取、未知字段、损坏、权限、symlink/hardlink 和实际 fd 身份均校验。journal 不保留完整 shell/env/stdin/文件内容/URL query；Guest 请求使用私有随机 key 的 HMAC 摘要，Host pending 仅保留必要的完整可审阅类型化参数。
7. open_url 只根据同项目、健康、generation 匹配的受控 exposure 构造 loopback URL，固定原生程序和 argv。P2 无 P4 来源时生产返回 exposure_unavailable；port/service driver 返回 unsupported；成功执行器路径用依赖注入验证，不提供模型可用的测试绕过。普通 Guest Shell 权限边界不扩大为 OS 沙箱。
8. Sol xhigh 实现；完成受影响检查、完整 Linux Guest check/lint、跨平台构建、并发/崩溃/短写/fsync/损坏/链接故障注入、真实原生双 workspace 和 Claude/Codex bridge/普通 Bash/fs 回归、全新独立审计及相关文档同步后，Luna 阶段提交。P2 不自动 push、不进入 P3。

影响：既有 managed 副作用需要明确初始化新 policy；旧记录可只读加载，unmanaged 行为保持。回退通过 revert P2 阶段提交并停用新增入口，保留项目、审计和用户数据；不降低 schema 或删除状态。

## 官方依据与版本边界

[OpenAI MCP 配置](https://learn.chatgpt.com/docs/extend/mcp?surface=cli) 描述 stdio 配置与工具白名单；实际适用的 0.148 字段已另用本机二进制确认。[客户端 tool search 协议](https://developers.openai.com/api/docs/guides/tools-tool-search) 支持离线实验中的发现流程，实验保留了真实调用结果。

[Claude CLI](https://code.claude.com/docs/en/cli-reference) 与 [MCP 文档](https://code.claude.com/docs/en/mcp) 描述严格配置入口；当前官网版本高于 2.1.220 的新增选项没有作为本机已支持能力。

[MCP stdio 传输](https://modelcontextprotocol.io/specification/2025-06-18/basic/transports) 和 [生命周期](https://modelcontextprotocol.io/specification/2025-06-18/basic/lifecycle) 为最小桥的协议依据。

[Apple clock_gettime 源码](https://raw.githubusercontent.com/apple-oss-distributions/Libc/main/gen/clock_gettime.c) 将 CLOCK_MONOTONIC_RAW 接到连续时钟；[x/sys v0.35.0 go.mod](https://go.googlesource.com/sys/+/refs/tags/v0.35.0/go.mod) 与 [Darwin 实现](https://go.googlesource.com/sys/+/refs/tags/v0.35.0/unix/zsyscall_darwin_arm64.go) 提供公开兼容调用。


## 实施补充（2026-11-06）

P2 已实际实现并完成完整 Guest check/lint、三平台构建、原生 Darwin policy、双 workspace 文件/拒绝/输入矩阵和真实双 harness Shell/native patch/MCP 验收；最新状态见 [验证记录](validation/P2.md)，实际契约见 [POLICY.md](POLICY.md)。首轮独立审计发现的 MCP runtime guard 锁存与未知结果响应问题均已修正，独立复审结论为可提交。阶段由 Luna 按最终冻结清单本地提交，不自动 push、不进入 P3。

预审 MCP 实验未覆盖 `include_local=false` 与固定 stdio 的组合。实施核对 0.148.0 固定源码与真实 harness 后，Codex 配置选择 reach executor：只在精确 binary/argv/cwd/env/绑定形态下进入 in-process typed MCP handler，不启用通用 local executor、不执行 Host shell。Claude 同样仅增加精确生成 bridge callback。MCP JSON 行立即透传，普通 Shell 保留 sentinel 机制；每连接最多一个固定 MCP 控制流，未知参数和其他 argv 不能获得 Host 权限。stdio/三工具/独立 operator 审批/单权威 ledger 的已审查架构不变。

最终证据根 `/Volumes/MPM/MCACHES/agentreach/p2-20261106-01a1ae61/evidence`；修正后审计清单与二进制 SHA 为 `audit-fixed-candidate-manifest.json`，最终提交清单为 `final-commit-manifest.json`，首轮原始候选和失败报告保留。两个新 fixture 容器和专用卷已精确清理，控制状态经逐项验证完整归档到外置 APFS；精确身份、归档路径及审计报告 SHA 见 P2 验证文档。原开发 Guest 和旧 P0 资源未重启/重建，不复用失效资源。
