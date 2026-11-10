# Runta 独立设备 token 的账户与计费只读 API

请实现后端并创建 PR，不要部署生产或合并。使用远端独立干净 checkout，先检查工作区归属、仓库约定和其他会话，不能修改 Forge 同步镜像或占用中的槽位。不启动子 agent。

## 目标
Fennec 使用 device-code 授权（目前复用 Errand client_id=runta_crew）。它需要通过 Runtime 公共 REST API 查询当前账号/组织的账户状态、余额、消费和资源计费用量，不依赖 Dashboard 浏览器 session。先实证检查当前 token 类型/scopes 和现有接口；“仅 Dashboard 可查”是待核实问题，不能仅凭猜测添加重复接口。

## 参考与范围
- Dashboard BillingPage.tsx、dashboard/src/lib/api.ts、billing-usage.ts。
- Dashboard /v1/organizations/{org_id}/billing/account、/billing、/billing/usage?window=7d|30d，以及 credit grants。
- public /v2/me 的 device-client identity、runta_crew token scopes；billing domain/shared application service；openapi/public.yaml 和 SDK 生成流程。
- 暴露账户状态（能否使用/限制原因）、币种、可用余额、赠送与付费 credits 的可辨认来源（沿用现有能力）、所选时间窗累计消费、CPU/Memory/Disk 计费用量和单位、统计起止时间、fresh/stale/unavailable/as_of。
- CPU vCPU-h、Memory/Disk GiB-h 是累计计费用量，不冒充实时资源利用率。金额精度和 minor units 必须清晰。
- 用户说默认赠送 $50 credits：不得硬编码余额或在查询时发放 credits；以账单服务实际记录为准。
- 复用 Dashboard 计费服务和缓存，不能复制 Metronome 逻辑或绕过授权。只读最小 scope，组织/用户隔离，明确组织选择；不能越权查询同组织不应可见数据或别的组织。
- 不新增充值、付款、订阅写权限。$5 Flash 订阅 PR 尚未合并，完全不依赖它、不开放购买入口。
- 保持 Dashboard 行为兼容；按仓库流程更新 OpenAPI、生成客户端/SDK、文档及有意义的契约测试。

## 验收
1. 明确 Fennec 如何拿组织 scope；提供推荐接口路径、请求响应示例及币种/单位/时间窗说明。
2. 实际 device-code token（runta_crew，以及其他支持的 device client 如适用）能读允许的数据；不足 scope/不同用户组织/过期撤销 token 正确拒绝。
3. 零余额与不可用必须不同；stale 不能冒充 fresh；credits 不重复统计；金额精度不丢失。
4. 与 Dashboard 相同组织/时间窗的余额和用量一致，允许已说明的缓存时间差。
5. 提交聚焦测试证据，创建 feature PR；不直接 push main/dev/integration，不擅自 merge/deploy。

## 交付
报告写到 /home/shiqi/fennec-device-billing-result.md，包含 PR 链接、commit、实际改动、验证证据、未验证边界、Fennec 接入示例。不要输出任何 token、cookie、完整密钥或敏感账单明细。
