# 干净环境 onboarding 审计：2026-11-11

最终用户验收尚未通过；本记录只描述本轮源码与隔离测试证据。
用户选择先完成开发，再安排干净 Mac/测试账号。当前本机 UTM 仅发现一台
已停止的 Windows VM，没有已准备好的干净 macOS 测试环境。

## 已发现并修复的开发环境依赖

1. `ensureControl` 原先只比较 build ID，App 移动后同一 binary 可能继续复用
   旧路径 daemon 和 LaunchAgent。现在同时检查实际执行路径及注册内容；
   旧路径/旧注册/无响应注册会在启动锁内被替换。兼容 symlink 不会误触发重启。
   回归覆盖移动路径、旧版本、旧协议缺执行路径、缺注册、转义路径及符号链接。
2. Swift 默认 deployment target 取决于构建主机。实际旧开发包的 UI/helper
   `minos=26.0`，已发布 0.2.11111055 的 UI/helper `minos=15.0`。
   现在固定编译 target 为 macOS 13.0，Info.plist 显式记录最低版本，包装
   校验检查所有 Mach-O 最低版本不高于 13.0；helper 缓存按 target 分隔。
   现有用户正在使用的 helper 未被替换，也未重置任何 Keychain 数据。

验证：`go test -race ./internal/cli ./internal/controlplane` 通过；macOS 13
Swift typecheck 通过；签名 staging 包 `/tmp/fennec-onboarding-os13/Fennec.app`
通过 `scripts/verify-macos-package.sh`。这不是 macOS 13 真实运行验收，也不是
已发布的新 DMG。日志：`/tmp/fennec-onboarding-os13-build.log`、
`/tmp/fennec-onboarding-os13-verify.log`。

## 卸载开发进度

已实现的底层组件：

- `internal/uninstall` 定义共用的确认与执行顺序：确认 → 停止自动恢复 →
  可选云端删除并确认完成 → 关闭开机启动 → 停止本机服务 → 恢复命令 →
  删除 Fennec 专属凭据 → 清理本机数据 → 删除自身 App。
- `internal/runta/DeleteOwnedShared` 对确认的同一 runtime 重新检查用户、owner、
  FENNEC_PURPOSE 和 revision；DELETE 接受不算完成，必须观察删除完成/404。
  不会用新 revision 自动重发 DELETE，也不会寻找并删除另一个 runtime。
- 本机计划仅枚举 Fennec 的目录及经过 bundle identifier 检查的 App；开发版
  归档和 helper 缓存限于当前 Fennec 仓库 build 目录，不删除源码或原生 Agent 凭据。
- 已实现 managed entry 恢复；如果其他包管理器已经更新了命令，则保留新命令。
  shell 清理只移除有唯一边界的 Fennec block，保留用户内容、权限和 dotfile symlink。
- 回归覆盖本机卸载不删除云端、错误确认零 mutation、云端失败时不清本机、
  删除接受后超时不算成功、归属/purpose/revision 变化拒绝删除、保留源码/native auth、
  外部命令更新和 shell 配置恢复。

`go test -race ./internal/uninstall ./internal/launcher ./internal/agent
./internal/runta ./internal/cli ./internal/controlplane` 通过，日志：
`/tmp/fennec-uninstall-first-batch.log`。所有删除测试均为临时目录/HTTP fixture。
没有执行任何真实云端删除或本机 Fennec 卸载。

必须继续完成：

- 共用 plan/execute/status 控制 API、`fen uninstall` 与安全 dry-run。
- 默认不选云端删除的危险 modal，明确绑定目标；CLI 显式确认参数。
- 持久化卸载意图、取消准备任务、阻止自动重建 runtime，未知结果恢复和重试。
- 独立自卸载执行器，确保删除自身后仍能完成清理；禁止误杀其他程序。
- 真实 login-item 注销、LaunchAgent 注销、Fennec Keychain 条目删除、WebKit/
  UserDefaults 清理，以及自定义 ZDOTDIR/XDG 的安装记录与发现。
- 最终签名 DMG、干净 Mac/新用户/系统重启及同事独立使用验收。

## 后续：共用卸载入口、恢复与移除 App 后的命令行为

以下为开发实现及回归证据，尚未做真实用户自卸载或删除真实云机器：

- 已接入设置页卸载 modal 与 `fen uninstall` 辅助命令，共用 Go 预览、确认
  和 worker 逻辑。默认只清理本机；可选云端删除必须输入具体 runtime ID。
  CLI 支持 `--dry-run`、`--yes`、`--delete-cloud --confirm-runtime <id>`。
- 预览绑定本机安装和源码 hash；执行前再次校验。恢复预览只能重新确认同一
  云端 ID/owner，不能转向替代 runtime。新 revision 必须经过新的预览和确认。
- 持久卸载标记位于 `.local/state/fennec-uninstall/active.json`，晚于 App
  清理才移除。它独立于普通设置目录，阻止失败后/重启后自动创建云机器。
  准备任务先取消并排空，普通 mutation 被暂停；重新登录和卸载恢复仍可使用。
- worker 从私有临时目录运行，签名 binary 字节与已确认 App 相同。清理范围
  包括当前注册的 bundle；进程匹配限制当前用户、精确 executable 路径，保留
  当前 CLI caller 和 worker。完成回执在本机清理及防重建标记移除之后发布。
- 云端失败、进程中断、凭据过期、旧预览、错误确认均保留恢复路径；不自动
  重发删除操作。UI 提供重新登录/重试；默认不展示内部状态码。
- App 消失后的 managed entry/shim 可运行已解析的原始 Agent，并明确告知
  当前在本机执行。参数、工作目录和退出码保留；App 仍存在时的云端失败绝不
  触发本机 fallback。原始 executable 发现会跳过其他 Fennec shim，防止递归。
- 设置页是主入口；Account 的独立“删除云电脑”入口仍需实现，不能把卸载弹窗
  中的可选项当作该独立功能的验收。

本轮证据：

- `/tmp/fennec-uninstall-review-tests.log`：launcher、agent、CLI、controlplane、
  uninstall、Runta 客户端 race 测试通过。
- `/tmp/fennec-uninstall-review-ui.log`：UI 检查及危险确认回归通过；包括默认不选
  云端删除、错误 runtime ID 不提交、过期 UI 回复不覆盖新选择、失败不自动重试。
- `/tmp/fennec-uninstall-review-vet.log`：Go vet 通过。
- 已成功跑过签名 staging 包的 `fen uninstall --dry-run`，仅生成预览，未调用
  worker。没有触发本机 Keychain 删除、服务注销、App 删除或任何真实云端 DELETE。

仍需实际验收：独立云端删除入口、完整自卸载/崩溃恢复、系统注销/重启、权限
与多副本的 SMAppService 行为、只拖入废纸篓后的后台注销、自定义 shell 安装
位置的持久记录、干净 Mac/正式 DMG/真实账号/独立同事完整流程。不得将上述
fixture、源码与 staging 结果标为最终用户验收完成。

## 后续：Launch at login 与后台启动分离

发现的隐藏行为：daemon plist 原来同时配置 RunAtLoad=true 和
KeepAlive.SuccessfulExit=false，因此即便用户关闭 UI 的 Launch at login，
后台注册仍会在系统登录时启动，并可能恢复账号/云连接。系统自带
launchd.plist 手册明确说明 SuccessfulExit 隐式要求 RunAtLoad。

修复为 RunAtLoad=false + KeepAlive.AfterInitialDemand=true。UI 或 CLI 的
明确启动执行 bootstrap 后 kickstart；首次需求之后保留异常退出自动恢复。
AfterInitialDemand 使用系统 SDK launch.h 定义的 key，未引入外部工具依赖。
原生 UI 在控制连接失败后重新进入启动流程，保留已有界面数据；卸载与启动
共用启动锁，启动方再次检查持久卸载标记，防止卸载过程中被 UI 自动拉起。

实测 `TestLaunchAgentWaitsForExplicitDemand` 通过（约 6.5 秒）：

1. 用独立的 `com.runta.fennec.test-demand.*` 临时 launchd label 注册无害 probe；
   注册后不执行。
2. 明确 kickstart 后执行。
3. 对该测试进程发送 SIGKILL，确认由 launchd 自动恢复。
4. bootout 后重新 bootstrap，确认不继承上次的 demand，不自动执行。
5. 清理并确认该测试注册已消失。

日志：`/tmp/fennec-login-policy-demand-recovery.log`。复现入口：
`make test-e2e-launchd-demand`，非 macOS 会失败；普通单元测试不运行该系统测试。
这不是完整的系统注销、重新登录、重启或干净 macOS 13 验收。用户当前
Fennec 服务、Keychain 和真实云电脑未被该 probe 操作。

另已修复前一提交的两个 gofmt 遗漏；修正提交 99ee63f 的 GitHub CI
42964771981 已通过。后续代码的 CI 仍须按新提交独立验证。

## 后续：过期登录与卸载恢复

发现：账号从 Keychain 恢复后即标为 authorized。旧 token 失效时，普通
Login() 的幂等判断会直接返回原状态，导致卸载恢复的登录按钮没有实际作用。

修复与开发证据：

- `account.login` 支持显式 `reauthenticate:true`，UI 卸载恢复使用该请求；
  CLI 对应 `fen login --reauth`。已在进行的授权仍合并为一个流程。
- 新授权完成前不删除旧的持久凭据，拒绝/取消不会擦掉恢复信息。
- 当前账号查询或运行时准备收到 401 时退回登录入口，并记录 session_expired；
  403/5xx 不触发登出。拒绝只作用于匹配 token 且匹配 epoch 的会话，迟到的
  旧响应不能登出更新后的登录，即使测试服务复用了相同 token 字符串。
- 卸载前排空账号写入、取消尚未完成的设备授权；进行中的卸载拒绝并发登录。
  失败后的重新授权仍可使用，持久卸载标记继续阻止自动重建云电脑。
- 删除前发现账号改变时返回可识别的恢复错误，危险目标仍绑定原 owner/ID。

`/tmp/fennec-auth-recovery-final-tests.log` 中 account、controlplane、CLI 和
Runta 客户端 race 测试通过；`make check`、UI 检查及 macOS 13 Swift typecheck
通过。认证测试使用 HTTP/内存凭据 fixture，没有修改用户真实登录或 Keychain。
仍需在最终签名 DMG 和专用用户上验证真实浏览器授权、Keychain 提示、取消及升级。

### Keychain 写入期间重复登录

`Reauthenticate` 原先先等待凭据写锁，再检查 authorizing；如果用户在 Keychain
保存尚未结束时重复点击，后一个请求可能等到前一个已变成 authorized 后又启动
新授权。现在先复用进行中的 flow，并记录请求 epoch；等待期间发生的更新登录/
退出也不会被迟到的请求覆盖。

新增受控凭据写入回归：真实设备流程 HTTP fixture 走到 token 保存阶段后暂停
写入，重复请求必须立即返回同一 authorizing epoch；释放写入后只存在一次授权。
`/tmp/fennec-keychain-coalescing-tests.log` 中 account race 测试通过；`make check`
通过。该测试未触发真实 Keychain 弹窗。前一提交 b8cbfd2 的 CI 42968246384 已通过。

## 后续：从 DMG 安装与分发权限

发现两个实际缺口：

- 原先可以从只读/临时 App 路径尝试注册后台，没有完整安装引导。
- 已发布 0.2.11111055 的 ZIP 解包后，App/Contents/可执行文件为 0700，UI
  资源为 0600：发布脚本保护签名材料的 umask 077 传播到了应用内容。
  这不是可供其他本机用户正常读取/执行的应用包权限。

本轮实现：

- Go 安装检查与复制逻辑，UI 启动前检查位置；需要安装时提供安装/选择目录/
  退出入口和进度窗口。CLI 对应 `fen install --check` 与 `fen install`。
- 签名通过后才发布复制结果；拒绝覆盖同名的其他应用，路径不能重叠或通过
  symlink 递归进入源 App。发布失败会尝试恢复旧版本；并发目标不被覆盖。
- 旧 bundle 保留在安装目录旁的 `.fennec-update-*`，保护仍映射旧 executable
  的 CLI。卸载清单包含已验证的安装备份，只移除空的备份父目录。
- 后台、内嵌终端和重启使用经过签名/原生 executable 摘要匹配的已知安装副本。
  UI 资源仍在自身 bundle 内。没有逆推出 Gatekeeper 的原始路径、清除 quarantine
  或关闭系统安全检查。设计参考 [Apple App Translocation Notes](https://developer.apple.com/forums/thread/724969)。
- 构建明确使用 public bundle 权限，并在签名/公证相关步骤检查；签名材料仍在
  bundle 外的私有目录。发布包的目录/程序为 0755、普通资源为 0644。
- 新增 `cmd/check-bundle-permissions`；发布流水线还会挂载最终 DMG，检查卷根目录、
  包内权限、代码签名、staple、Gatekeeper 及 CLI 安装检查，而非只检查 staging。
- `make test-e2e-installation FENNEC_ACCEPTANCE_APP=/path/Fennec.app` 用 Go 驱动实际
  只读磁盘镜像、签名复制、升级备份和权限检查。子进程只使用系统 PATH。

证据：`/tmp/fennec-install-final-gate-tests.log`、
`/tmp/fennec-install-final-gate-verify.log`、
`/tmp/fennec-install-final-gate-probe.json`。
受限 umask 和系统 PATH 下的隔离复制测试通过，当前 LaunchAgent 注册内容未变。
未安装到真实 Applications、未启动新 UI/后台、未访问 Keychain 或云端资源。

这仍不是干净设备的最终验收。Safari 下载产生的 quarantine、真实 Gatekeeper
首开、安装 UI 的实际点击/移交、其他 OS 用户的运行、系统登录/重启，以及最终
公证 DMG 的完整首次使用，都仍需按 TODO 验证；本轮没有发布新的正式 DMG。

## 原生模型边界与展开面板验收

- Magpie 关闭时，新增常驻 channel 的 `native_models` / `native_model_set` 操作；
  UI 与 `fen model <agent> [model|default] [effort]` 使用同一 Go 控制面。
- 模型目录来自隔离的原生 Agent 缓存；Pi 只列出已配置 provider 的模型。
  云端校验目录成员与 effort，跨厂商遗留选择清除为 Agent 默认值，不修改本机配置。
- 展开面板改为完整宽度的 Model 控件、模型图标及有元数据支持时的 effort 滑杆。
- 实测发现 Codex 写入 `[tui]` 后，旧同步基线误判冲突。修复仅允许保留云端生成的
  TUI/notice/projects 附加字段和有效模型选择；provider 配置冲突仍拒绝覆盖。
- 已在 canonical 开发 App 上重建并验证：原生 Codex 返回 GPT 模型目录，界面显示
  GPT-6.1-Sol / low，菜单没有 Kimi；CLI 拒绝把 Kimi 设为原生 Codex 模型。
  Claude 目录只含原生 aliases；Pi 无已连接原生 provider 时只显示 Default，
  不再展示遗留的 Magpie 当前模型。
- 测试期间临时关闭 Magpie，结束后恢复原先的开启设置；Claude/Codex/Pi 均恢复
  configured、broker ready。未执行模型推理，未删除或停止用户会话。
- 本地安装继续使用原始已签名 credential helper，SHA-256 保持
  b0ee08622cd866bf1e1bfd7c5f7f76ba7eca51944364e329a6d47a33e3714d6e；完整 bundle
  签名验证通过。仅本机开发包复用该 helper，不把它当作 macOS 13 分发验收证据。
- `make check`、`make check-ui`、Native setup race 测试，以及 runtime/cli/terminalstream
  race 测试通过。完整 setup 测试仍有三组安装用例因 macOS 缺少 flock 失败。

证据在 `/tmp/fennec-native-tests.log`、`/tmp/fennec-model-control-tests.log`、
`/tmp/fennec-native-codex-models.json`、`/tmp/fennec-models-restored-state.json`。
尚未发布新 DMG；干净 Mac 首次安装与全部 onboarding 验收仍未完成。

## 原生 OAuth 与遗留网关配置隔离

继续审计发现，已有原生 OAuth 时，本地 localhost gateway 配置仍会导致
`local_provider_unavailable`。新增源端导出、云端应用和执行环境三处约束：
有原生 OAuth 身份才移除代理 endpoint/key 覆盖；没有原生 OAuth 时不会把
代理 API key 改送官方服务。Claude 环境变量及 settings.env 内的显式 OAuth
均覆盖；共享环境中其他 Agent 的凭据不再覆盖原生身份。

回归覆盖：Codex/Claude saved OAuth、Claude 环境变量/配置内 OAuth、代理 API
identity 带旧 OAuth 字段、旧客户端导出、云端 TUI 配置保留、跨 Agent 环境隔离。
`/tmp/fennec-native-identity-tests.log` 为 Native setup race 测试证据。

开发机只读凭据形状审计确认：当前 Claude 无 saved/environment 原生 OAuth，
Pi 的原生 auth provider 列表为空。因此它们在关闭 Magpie 后尚不可用，不能把
fixture 的 OAuth 成功用例当作这两个实际账号的原生登录验收。真实开发包关闭
Magpie 时 Codex configured；恢复原先 Magpie 开启设置后三个 Agent 均 configured，
broker ready。记录为 `/tmp/fennec-native-identity-live-off.json` 与
`/tmp/fennec-native-identity-live-restored.json`。未运行模型推理或停止用户会话。

尚需产品内原生登录入口、专用原生测试账号和最终 DMG/干净 Mac 验收；TODO
对应项保持未完成。此次仅为开发与开发机验证，不是最终用户交付完成证据。

## 原生账户入口与 UI 收敛（开发候选，未完成最终验收）

按后续用户要求更新产品边界：首次初始化检测到实际安装的 Magpie 时默认开启，
未安装则关闭，保存过的手动选择优先；正式 UI 不再提供卸载入口或卸载弹窗，
`fen uninstall` 保留为 clean macOS 测试工具。这些要求取代本文件较早的默认值
和卸载 UI 描述，TODO 的最终验收项仍全部保留。

本轮实现：

- 原生账户信息经 `agent.account.get` / `fen agent account` 读取，本机与云端按
  运行位置区分，返回白名单字段（账户邮箱、认证类型、明确的套餐名称），不返回
  token、API key 或凭空推断额度。显式 Claude OAuth token 不沿用另一登录文件的
  邮箱或套餐。无可迁移凭据正常返回未登录，单个 Agent 不阻塞其他 Agent。
- 原生 cloud 登录经 `agent.login.start/finish/cancel` 与 `fen agent login` 共用
  Go 控制面和常驻协议 channel。云端调用已安装的原生 CLI，在独立目录中登录，
  完成后原子选择该账户。旧请求、取消请求不能迟到覆盖身份，本地凭据同步不覆盖
  用户明确选择的云端账户。确认登录进程停止后清除取消尝试的临时凭据。
- daemon 从原有会话列表响应中的公开登录 ID 识别完成状态，无需 UI 回调或新建
  SSH channel；按用户及 runtime 保存源选择提示并重新确认配置。
- 缺少凭据等稳定状态允许复用准备结果，不再因此反复安装环境；真实安装失败和
  未确认的凭据读取失败仍不能缓存为成功。
- 原生模式显示账户订阅，Magpie 模式提供对应本机/云端的模型选择，本机选择不会
  错写云端。保存反馈留在原控件内，滑杆改为细灰轨道、离散刻度与白色圆形滑块，
  交互期间不替换正在拖动的控件。界面文案遵循不使用句号的项目规则。
- 会话页不再把 Agent 未登录汇总成全局环境错误，先渲染 Agent 结果再渲染准备状态。
- 构建时重新复制 UI 资源，删除的页面脚本不会残留在重复使用的 staging bundle。

开发验证：Native setup、CLI、runtime、terminalstream、controlplane、uninstall
相关 Go/race 测试，以及 `make check` / `make check-ui` 已分别执行。完整 setup
测试在本机仍有已有的 flock 安装用例失败，须由 Linux CI 再验证。
证据包括 `/tmp/fennec-onboarding-final-focused.log`、
`/tmp/fennec-final-control-tests.log`、`/tmp/fennec-scope-cache-tests.log`。

限制：原生登录的 subprocess 测试使用独立 Go fixture agent，并非真实订阅登录。
尚缺专用账号完成 Codex/Claude/Pi 实际登录、取消、过期和响应任务的完整验收；
staging 的构建/签名检查不能替代最终 CI DMG、干净 Mac、系统重启或独立用户验收。
此次没有删除真实云电脑，也未将开发签名包替换到当前正式安装的 /Applications。
