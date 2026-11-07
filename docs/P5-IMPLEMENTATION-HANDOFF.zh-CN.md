# AgentReach P5 新 session 开工交接

本文件由用户要求创建，供项目内新的 GPT-6.1 Sol / xhigh session 直接开工。目标是完成主计划第10节的 P5：只读 reconciliation、显式迁移与重绑定、受控 runtime 生命周期、并发/中断恢复、孤儿治理、真实备份还原及升级兼容验证。不能只交设计或内部 API。

## 最新授权与停点

- 用户原文：“在项目内创建一个新 session，把方案和必要的交接项提供给新 session 的模型，让它开始跑 p5 目标，模型使用 6.1 sol xhigh”。这是本轮 P5 开工授权；按主计划与本交接的范围实施，先简短说明关键调用链、首选方案与待实验假设，然后继续执行，不重复询问是否开始 P5。
- 新 session 必须使用 **GPT-6.1 Sol（gpt-6.1-sol），reasoning xhigh**，自身承担实质实现。可委派有界分析；重要代码验证后，由未参与实现/调试的代理独立审计。阶段完成、验证、审计、冻结清单后，交 Luna 本地提交，主 session 独立核对 parent/blobs/status。不递归派发整套工程，不因额度失败不断换代理或替换指定实现模型。
- 本次授权止于 P5 与最终本地阶段提交。用户此前对 P4 的明确推送授权已经执行，**不自动扩大为 P5 push**；不自动创建 PR、切分支、建 worktree、迁移仓库、覆盖全局安装或开始其他阶段。
- 实质改变范围、架构、安全契约、数据兼容或新增高影响操作时，先提供可审阅方案并等用户明确确认。已授权方案内的普通实现细节自行解决。规则约束 Agent 工作，不自动成为产品里的审批页面或新增确认开关。
- 只能对本任务新建、记录完整身份的隔离测试资源做 restart/replace/rebind/restore。不得把生命周期开工授权当作操作开发 Guest、旧 P0、用户既有项目或共享资源的许可。
- 旧 P4 session 完成交接后停止修改仓库；新 session 是这份 checkout 的唯一实现者。不必给旧聊天或其他人员发消息，直接向用户报告。

## 阅读顺序与实际 Git 基线

先读本文件、[主计划](POST-P0-DEVELOPMENT-PLAN.zh-CN.md)第2–5、10–16节、[MANAGED-PROJECTS.md](MANAGED-PROJECTS.md)、[POLICY.md](POLICY.md)、[TERMINALS.md](TERMINALS.md)、[SERVICES.md](SERVICES.md)及 [P4 验证记录](validation/P4.md)，再追代码。主计划里历史“不进入 P5”停点由本次用户授权更新；历史拟议命令仍不是现有接口。

- 仓库 `/Volumes/MPM/sth/agentreach`，分支 `codex/orbstack-execution-plane`。
- 当前基线 P4 commit **`d1f88fd0655f51dd970ee7f1d6ee4281a2303b9c`**，唯一 parent `e40092e18d4fc999952454425a170d30b86fd15b`。P4 的47个 committed blob 和工作树已逐项与冻结清单核对，提交后工作区干净。
- fork `origin` 为 `https://github.com/maotsuai/agentreach.git`，`upstream` 为 `https://github.com/bojieli/agentreach.git`。P4 已非 force 推送到 fork 的 `refs/heads/codex/orbstack-execution-plane`；本次交接又实际 ls-remote 确认仍为上述 P4 SHA。不得推 upstream。
- 本文件是本轮唯一新交接产物，创建新 session 前应为唯一 untracked 文件，保留并随 P5 交付；开工仍须重新核对真实 status，保护后来出现的改动。不 reset/clean、add -A、amend、跳过 hooks 或改 Git 身份。

P4 证据根为 `/Volumes/MPM/MCACHES/agentreach/p4-20261106-01a1b154/evidence`：

| 证据 | SHA256 |
| --- | --- |
| release-commit-manifest.json | eefe0f8fb79a9ec8a1ed4510ea92ea5cb8144d5172c9683210ebad32ba906c8f |
| parent-commit-verification.json | 584770a15c400070d35b080c8b41036391a945bf50aea61dd9469cd3b05100db |
| push-verification.json | 099129256ca1b8b2ab75d5214b7166d07d07322cb114161b750c4f0b28061e92 |
| independent-release-local-audit-20261107.md | 3ea8ef3476c1c1cacfc1ee170159321a6a2dee1ddb27171f914d18376c7dc2f7 |

无需再次提交或推送 P4；这些是历史基线证明，不能替代 P5 修改后的验证。

## 环境、存储与当前资源

本次交接已只读核对以下事实，后续使用前补核实际状态：

- MPM：挂载点 `/Volumes/MPM`，ExFAT，UUID `C48A19AC-6C78-34CA-8969-BD408C67CAFE`；APAP：挂载点 `/Volumes/APAP`，APFS，UUID `D1814C29-318B-418F-B5F6-1F7E324E5B42`。路径不存在/卷身份变化即停止依赖该路径的动作，禁止写空挂载点或静默回退内置盘。
- 既有 Linux dev Guest 完整 ID `622e55362ab130301a112feea6499b62fff1313919502faac9775dbc4fa83b6c`，running，StartedAt `2026-10-31T03:01:41.099068654Z`，本地 image `sha256:c9f6a16b80584a752f2e3d06a32b0882e3e3d7bfd934ee1d2f624b560f9e1183`，UID/GID `10001:10001`。**不得重启或重建该 Guest。**
- 源码 `/workspace/agentreach` bind RW，嵌套 `.git` RO。所有 Go fmt/vet/test/build/lint 和开发依赖操作只在这个 Guest，使用 `docker --context orbstack exec <完整ID> bash -c 'cd /workspace/agentreach && ...'`，不使用 login shell。Host 只编辑/只读调查、执行受控操作及运行 Guest 交叉构建的原生候选。
- P4 已验证 Go1.25.8 / GOTOOLCHAIN=local，golangci-lint2.12.2 位于 `/home/developer/.local/bin/golangci-lint`，tmux3.3a。cache/module/GOPATH 均在外置 engine；OrbStack 实际 data_dir 为 `/Volumes/APAP/vm/Orbstacks`，须再次读真实设置及实际落点。
- P5 普通候选/证据使用 `/Volumes/MPM/MCACHES/agentreach/p5-<新任务ID>`；private REACH_HOME、socket、权限敏感控制/备份放 `/Volumes/APAP/MCACHES/agentreach/p5-<同任务ID>`。不要重用 P4 REACH_HOME 做 schema 迁移或重写 P4 key/ledger。普通资源优先 MPM，只有确需 APFS/私有权限等必要部分放 APAP。
- P4 private 根 `/Volumes/APAP/MCACHES/agentreach/p4-20261106-01a1b154`，REACH_HOME 为其 `c` 子目录，含 HMAC key，禁止输出或复制到仓库/普通证据。P4 状态和归档保留，不自动清理共享缓存。
- 旧 P0 `670140ac13d8ad3fa9b7e1696f67a0328faae646e724fea74071b3898084b799` 仍 running，StartedAt `2026-10-31T15:30:32.23468246Z`，只读观察，不作故障夹具。

P4 保留的健康资源（用于以后补外部验收，不作为 P5 破坏性实验对象）：

| 对象 | 当前精确身份 |
| --- | --- |
| shared workspace | `3c6d5e40ca04173698a6660914d0cc43dd63630cfc261092b46d1385281f129f`，project `p4-shared-01a1b154`，StartedAt `2026-11-06T15:12:25.446805907Z` |
| shared network | `65d47d3c0e1a6fec60c432ad4fe89226504006765342c410a96264e51b49f170` |
| shared WebUI sidecar | `7742f6708f4efda0135e3c1bf3387e3026e1d2717f6f62890c4d167817314e3d`，service `b407056ee27681095967b19860ec1f78`，generation2，StartedAt `2026-11-06T18:31:30.887332548Z`，127.0.0.1:48145；交接时真实HTTP200 |

P4 owned runtime `095dd730b7c2c9f611b9cecfc13e43ba9932ed8f7b7a3d4d1ea88677ac39c967` 及替代 ID `2ca01bc2b66c8a62a0ee34c8004cec35a16321f07fd6aacb21980fd19f129871` 均已退役删除，原项目故意失效；不要直接重跑默认含 owned 的旧脚本。原卷 `reach-p4-owned-01a1b154` 与 workspace 归档保留，不随手删。完整身份/清理证据见 P4 `resources.json`、`native-lifecycle.json`、`retirement-cleanup.json`。

## 已交付能力及 P5 必须保持的不变量

- P0/P1：固定 daemon/容器/StartedAt/snapshot、外置盘 guard、PathMapper/rooted helper、held-fd cwd 启动及 managed 环境约束保持；legacy Local/SSH/Podman/unmanaged Docker 不回归。普通 Guest shell 不是 OS 沙箱。
- P2：Host 默认 deny；严格类型请求、精确 plan/approval、审批消费与 durable intent 同次持久化；result失败/派发不确定必须保留原 operation ID，unknown 不重放。mandatory security ledger 不受 REACH_NO_AUDIT 关闭，不记录原始 Guest stdin/argv/env/HTTP body/Cookie/URL secrets。
- P3：terminal/runtime/server/pane/nonce 全身份与日志、input去重、lost不复活；workspace重启不恢复旧内存。close不保证 detached 后代终止。
- P4：sidecar private generated Compose，可信CLI/plugin，create→完整配置核验→exact-ID start；无任意仓库 Compose/build/pull/Host shell。workspace service用私有 service-backends，不开放交互 write；stop返回 descendants=unconfirmed。
- 生产 open_url 只能由同 binding/generation 的新鲜 exposure 生成受限loopback origin，执行前复核。动态 proxy真持有 net.Listen 的 listener，关闭/撤销处理已有流，私有socket/nonce，不凭PID文件 kill。
- 动态暴露/拨号前有完整readiness；拨号后首payload前及活跃流仅检查完整runtime/process/endpoint身份，避免单线程HTTP被第二个health请求卡死。不能把P5改动退回旧B4缺陷。
- 每个 MCP 连接的首次 runtime/identity guard 失败永久锁存。只读重试或新session不能让已lost的旧对象复活；普通policy/readiness业务错误不得全部归为身份失效。
- 私有状态严格解析、owner/权限/link检查、原子 envelope、有界锁与预算保持。不要通过删坏状态、吞错、增加无界重试或自动偷锁“修复”。

## 当前真实代码链路与 P5 切入点

以下已读原文件，不是仅据旧摘要：

1. `internal/project/project.go`：`Schema=1`；Load拒绝非revision/generation1；Register只写一次，既有项目不可覆盖。`orbstack.Guard.Bind` 当前产生固定1/1。
2. `internal/session/managed.go`：Managed保存完整project Record的SHA；ManagedProject/CheckManaged每次比对记录、session目标/创建身份和guard；policy/terminal/service通过这个校验注入能力。
3. `internal/ports/ports.go` 的Config.validate仍要求 Binding.Generation==1；policy记录、terminal/service状态均绑定完整policy.Binding。P5不能只改project.Load一个判断而遗漏生产路径。
4. `cmd/reach/project.go` 当前只有register/inspect；`internal/host/execute.go` 的非open_url调用交 service.ExecuteHost，runtime_change仍无生产driver。需要明确区分runtime与service派发，不能悄悄放行原来的unsupported。
5. `internal/state`、`internal/privatecontrol`、`internal/policy` 分别掌握严格IO/原子写、私有envelope/锁和mandatory journal；优先复用适用能力，不为P5另造无关通用框架。
6. P5跨generation还涉及 `internal/session/{policy,terminal,service}.go`、`internal/runtime/{docker,orbstack}`、`internal/terminal`、`internal/service`、`internal/ports`、`internal/host` 及CLI/MCP/harness接入；新控制目录必须纳入CheckControlPaths/hostpath保护。

## 本轮细化实施方案（首选顺序）

### P5-A：只读 reconciliation 与稳定计划

先实现可实际调用的只读计划入口（主计划示例 runtime reconcile --plan / project rebind --plan，具体CLI语法由实现冻结并同步文档）。比较Host期望记录、project/policy revision、完整Docker运行身份/配置、terminal/service/exposure、卷/engine位置、未完成intent/孤儿/缺失/未知ownership。

输出 desired/observed/difference、关联对象、风险、建议动作及稳定plan digest、expected_record_digest/expected_revision、期限和固定effect集合。未知ownership只报告，不自动认领或删。read-only plan不能创建Host状态、mkdir bind source、start/pull/build/repair；用前后资源/文件快照和调用计数证明零mutation。

### P5-B：project v2、显式迁移与单一权威状态

首选显式v2契约：revision记录授权状态修改，generation记录执行实例身份变化；旧v1保持只读兼容且不在inspect时迁移，旧二进制拒绝v2。新增CAS以expected_record_digest+expected_revision为准；两个并发apply最多一个成功，任何计划所依据状态变化均plan_stale，不能重新算计划继续使用旧审批。

先用最小实验验证多记录迁移在每个写盘/dispatch边界中断后如何唯一恢复，再落实数据布局。优先单一权威提交点配合有界migration intent/备份/完成标记；不是多个JSON文件依次rename就声称跨文件原子。保留原记录/历史证据，结果未知保留operation ID。恢复不能猜“mtime最新文件”，不能把v2删字段伪装回v1。

### P5-C：显式受控 runtime 生命周期及新绑定

为 start/stop/restart/provision/rebind 建闭合规格、影响预览及P2 runtime_change生产执行链。控制面必须能在旧runtime已不可用时合法诊断/批准恢复，但这只能是经过明确权限、控制根/存储/engine身份核验的专属恢复路径，不能弱化所有CheckManaged或加入公开skip_guard。

apply引用原plan/digest/revision/期限，在锁/CAS与实际派发前重新观察，durable intent先于effect，只对精确批准ID动作。start/restart前重新验证外置卷、engine与规格；restart改变StartedAt后返回明确的新绑定计划，不自动恢复session。provision批准镜像/网络/卷/保留策略，不补缺失bind source，不把health变成provision。

提交新绑定后，旧session/child/terminal/service/exposure和旧approval全部失效；旧记录/安全journal保留，不能把旧对象引用改指新runtime。新session/对象显式创建。注意旧manager实际listener/连接的撤销与重核、连接锁存、policy授权epoch，以及新generation的policy/terminal/service/ports生产路径都需闭环验证。

### P5-D：并发、中断恢复和孤儿治理

覆盖主计划10.4全部事件，优先受控注入：engine暂不可达、同ID重启/同名替代、磁盘满/atomic写失败、派发后result失败、记录损坏、plan过期、并发CAS、迁移各落盘边界中断、manager崩溃/孤儿。

孤儿cleanup需dry-run→明确plan→批准apply，绑定exact ID/operation/ownership，旧计划或未知ownership拒绝。P4目录锁在持锁SIGKILL后可能遗留；设计恢复时不能仅凭timeout/旧PID偷锁。不能证明安全自动恢复的情形，应保留诊断与具体可审阅的operator恢复步骤，不把人工条件省略成自动保证。

### P5-E：备份/还原与隔离升级演练

数据备份与控制状态分开；默认不明文备份模型凭据/HMAC key。备份manifest、身份、校验和、完整性及一致性策略应明确。拒绝归档路径穿越/symlink/hardlink等逃逸，不能把不可信tar自动展开到Host任意目录。

在新随机容器/卷/项目ID恢复，不覆盖原对象；真实写入代表性数据后导出，恢复后检查内容/校验和及服务实际运行。恢复不是还原旧ID/approval/terminal内存，必须重新登记和批准。不得只靠data.img.raw，禁止手改OrbStack内部磁盘。

升级演练先针对隔离reach/helper/控制schema和固定harness候选，保存版本/hash与实际seam结果；旧verdict不能跨版本复用。不要升级全局OrbStack/Claude/Codex或重启engine来制造验收。需要这些操作时另报具体窗口与回退方案。不可执行的真实硬件/系统事件按“未执行”逐项交付，不能以模拟冒充实际。

### P5-F：回归、文档、独立审计与阶段提交

补齐用户真实入口、MANAGED/POLICY/TERMINALS/SERVICES契约及恢复/备份runbook，更新主计划状态、新建docs/validation/P5.md，分开已实现、局部测试、真实验收、独立审计和未执行项。

完整Guest make check（vet+全包race/count1）、make lint、必要Linux/Darwin/Windows候选构建；实际原生macOS和两种workspace模式、真实固定Claude/Codex+localhost模型mock的P0–P4回归和P5接缝。不要运行付费make e2e。通过后独立审计、核对修复重验、冻结精确文件清单，Luna本地提交并由主session复核；本次不自动push P5。

## 本批验收与回退

| 验收 | 客观证据 |
| --- | --- |
| 只读plan零mutation | 文件/资源前后快照、固定driver调用、坏状态也无创建/修复 |
| R01/CAS/过期计划 | 两进程同expected状态最多一成功；旧digest/revision/期限、锁内变化均拒绝且零额外effect |
| 迁移与崩溃恢复 | 每个真实提交/派发边界注入，始终只有一个权威binding，operation不丢/不重放，v1/v2兼容及旧binary拒绝有实际证据 |
| 旧代次全面失效 | 旧session/child/FileOps/MCP/terminal/service/exposure/approval均拒绝或按明确策略关闭；新session在新绑定可用 |
| 真实runtime生命周期 | 两种新隔离fixture中精确stop/start/restart/rebind/provision，配置核验和新StartedAt不自动认领 |
| 故障与孤儿 | desired/observed/error正确，dry-run/apply权限与ownership守住，未知结果仍有原ID |
| R02备份还原 | 独立新项目/卷中的实际内容/校验和及可访问服务，原资源未覆盖，无旧批准复用 |
| 升级与回归 | 精确候选版本/hash、实际Claude/Codex离线模型seam，P0–P4原入口不回归 |

回退：停用P5入口/按阶段commit revert；对v2用兼容二进制或经审阅控制备份恢复，重新核验资源，不强制降schema。精确关闭本次自有对象，保留数据/ledger/未知结果/必要证据。破坏性reset/prune/down-v、清数据、物理拔盘、系统睡眠、全局engine重启、OrbStack Storage迁移不属于普通repair，需独立明确授权；不在无人值守情况下自行操作。

## P4 证据复用及否定结论

- 最后完整检查 `full-check-http-identity-fixed.json` exit0，原始log SHA256 `d673b555170c5bf3df8e2f3e5331a8236dcaaa473b0105420c631d411c26dfa5`；P4最终Darwin候选 `/Volumes/MPM/MCACHES/agentreach/p4-20261106-01a1b154/bin/reach-darwin-http-identity-final` SHA256 `10212ab6e6221f606269518b559dfd9a0e8d1d324245780fb2442b4745395a43`。旧的reach-darwin-final为修复前版本，不能混用。
- 真实harness基线：Claude Code2.1.220，`/Users/nyan/.local/lib/node_modules/@anthropic-ai/claude-code/bin/claude.exe`，SHA256 `8addc857f3fe64d5a0368af9ee50321b50afb4a6918ba3ef018ab84f5dbbe081`；Codex0.148.0，`/Volumes/MPM/MCACHES/agentreach/host-test-tools/codex-0.148.0/codex`，SHA256 `b0308517b20543012fa2171aa3d46ce455a7456c4eb2a552ab9468ba4eeb1e50`。使用前核验，不覆盖全局版本。
- B1并发重复请求丢operation ID、B2健康观察排队误关listener、B3重复receipt绕过身份锁存、B4 postdial/monitor的HTTP health死锁均已修复并独立回归；原失败、overlay及最终10用例日志保留在P4 independent-audit目录。P5改绑定/锁序时重点防回归。
- TCP撤销允许EOF或reset；不能只接受EOF把已关闭误判为失败。刚关闭连接后的Python无SO_REUSEADDR bind曾EADDRINUSE，后续无listener/两种reuse可bind补核分开记录，未证明TIME_WAIT根因。
- 原生manager崩溃只覆盖空闲、无观察资源锁的SIGKILL，不证明持锁崩溃透明恢复。P4已有原样保留并经精确inode人工归档的废弃锁证据，不能当产品自动偷锁先例。
- P4按用户本机范围交付：真实VPN Disconnected→Connected→Disconnected，默认route en1→utun11→en1，backend始终bridge104；固定/动态六次HTTP body一致。本机LAN地址拒绝不等于独立LAN设备负例；Guest IP/.orb.local真实可旁路。
- 当前OrbStack LAN配置读false，UI曾显示Apply and Restart但未点击，engine未重启，不能宣称运行时LAN隔离已验。VPN已恢复Disconnected。独立LAN、强制VPN route outage、ApplyRestart后隔离仍未验；本机sudo -n要求密码，未索取密码/增权，不借P5偷偷补做系统变更。

## 开工后的最小动作

1. 核对本文件、Git/卷/Guest、P4保留资源和P5新隔离根；本文件之外若有新增改动先判归属。
2. 追project→session/guard→policy→terminal/service/ports以及CLI/Host执行器，列清v2/CAS、失效传播和恢复权限的实际衔接。
3. 先实现/验证P5-A只读plan及零mutation，再做多记录中断最小实验与v2/CAS；不要一开始就在原项目改schema。
4. 按A→F持续完成，独立测试可并行批量收取，依赖/写入/审批顺序执行；状态未知先调查再重试。每约60秒给有实质内容的中文进度，完成后直接向用户交付。

以上是本次交接的全部运行中工作状态：没有构建、harness或Host manager需要接管；P4保留WebUI仍在运行，开发/P0均保持原代次。新session开工时再核实，旧session不会并行实现。
