// 审计动作的唯一事实源。
//
// 收敛前同一个动作集合抄在三处：lib/audit.ts 的 AUDIT_ACTIONS（筛选白名单）、
// lib/audit.ts 的 LABELS（英文标签）、app/components/AuditClient.tsx 的 ACTIONS
// （中文下拉）。三处各自演化的结果是真的：model.catalog.sync 两个变体在
// lib/catalog-refresh.ts 与 app/api/admin/models/sync/route.ts 里持续写入，
// 但三个清单里都没有它——管理员在下拉里选不到，按 action 筛选也永远返回空，
// 于是"目录同步失败"这类事故只能靠肉眼翻全表发现。
//
// 现在只改这一处：服务端白名单/标签、客户端下拉、以及 lib/audit-actions.test.ts
// 的漂移门禁全部从这里派生。新增动作时如果忘了登记，测试直接红。
//
// 本模块必须保持零依赖（不碰 prisma / next）：AuditClient 是 "use client"，
// 任何服务端依赖都会被拖进浏览器包。
export type AuditActionSpec = {
  readonly value: string;
  /** 英文标签：审计导出与英文界面使用。 */
  readonly en: string;
  /** 中文标签：管理台下拉使用。 */
  readonly zh: string;
};

export const AUDIT_ACTION_CATALOG: readonly AuditActionSpec[] = [
  { value: "auth.login", en: "Signed in", zh: "登录" },
  { value: "auth.login_failed", en: "Sign-in failed", zh: "登录失败" },
  { value: "auth.logout", en: "Signed out", zh: "登出" },
  { value: "user.password_self", en: "Changed own password", zh: "修改本人密码" },
  { value: "key.issue", en: "Issued key", zh: "签发密钥" },
  { value: "key.revoke", en: "Revoked key", zh: "撤销密钥" },
  { value: "distribution.create", en: "Created distribution", zh: "新建分发 API" },
  { value: "distribution.update", en: "Updated distribution", zh: "更新分发 API" },
  { value: "distribution.assign", en: "Assigned distribution", zh: "分发 API 分配给员工" },
  { value: "distribution.revoke", en: "Revoked distribution", zh: "撤销分发 API" },
  { value: "key.auth_failed", en: "Key auth failed", zh: "密钥鉴权失败" },
  { value: "key.model_denied", en: "Model denied", zh: "模型被白名单拒绝" },
  { value: "key.rate_limited", en: "Rate limited", zh: "触发限流" },
  { value: "key.rate_limit_unavailable", en: "Rate limit unavailable", zh: "限额后端不可用（fail-closed 拒绝）" },
  { value: "key.overloaded", en: "Overloaded", zh: "过载拒绝" },
  { value: "user.create", en: "Created user", zh: "新建成员" },
  { value: "user.update", en: "Updated user", zh: "更新成员" },
  { value: "dept.create", en: "Created department", zh: "新建部门" },
  { value: "dept.delete", en: "Deleted department", zh: "删除部门" },
  { value: "provider.create", en: "Created provider", zh: "新建供应商" },
  { value: "provider.update", en: "Updated provider", zh: "更新供应商" },
  { value: "provider.test", en: "Provider test", zh: "供应商连通性测试" },
  { value: "model.create", en: "Created model mapping", zh: "新建模型映射" },
  { value: "model.catalog.sync", en: "Synced model catalog", zh: "同步模型目录" },
  { value: "model.catalog.sync_failed", en: "Model catalog sync failed", zh: "模型目录同步失败" },
  { value: "budget.upsert", en: "Set budget", zh: "设置预算" },
  { value: "budget.delete", en: "Deleted budget", zh: "删除预算" },
  { value: "limit.upsert", en: "Set rate limit", zh: "设置限流" },
  { value: "limit.delete", en: "Removed rate limit", zh: "移除限流" },
  { value: "price.publish", en: "Published price version", zh: "发布价格版本" },
  { value: "security.csrf_rejected", en: "CSRF rejected", zh: "CSRF 拒绝" },
  { value: "request.duplicate", en: "Duplicate request rejected", zh: "重复请求拒绝" },
  { value: "redact.rule_create", en: "Created redaction rule", zh: "新建脱敏规则" },
  { value: "redact.rule_update", en: "Updated redaction rule", zh: "更新脱敏规则" },
  { value: "redact.rule_delete", en: "Deleted redaction rule", zh: "删除脱敏规则" },
  { value: "masking.blocked", en: "Masking blocked request", zh: "脱敏拦截请求" },
  { value: "masking.degraded", en: "Masking degraded", zh: "脱敏降级" },
  { value: "masking.bypass", en: "Masking bypassed", zh: "脱敏被绕过" },
  { value: "diagnostics.export", en: "Exported diagnostics", zh: "导出诊断包" },
  { value: "license.import", en: "Imported license", zh: "导入授权" },
  { value: "license.revocation_import", en: "Imported revocation list", zh: "导入吊销列表" },
  { value: "license.invalid_signature", en: "Invalid license signature", zh: "授权签名无效" },
  { value: "license.clock_rollback", en: "License clock rollback", zh: "授权时钟回拨" },
  { value: "license.read_only", en: "License read-only rejection", zh: "授权只读拒绝" },
  // 维护平面 P0（FR-60—67）。维护关闭时这些动作不会出现；登记是为了
  // 审计页筛选白名单与导出表头在维护开启后立即完整（RM-A07）。
  { value: "maintenance.ticket_approve", en: "Approved maintenance ticket", zh: "批准维护工单" },
  { value: "maintenance.ticket_revoke", en: "Revoked maintenance ticket", zh: "撤销维护工单" },
  { value: "maintenance.session_connect", en: "Maintenance session connected", zh: "维护会话已建立" },
  { value: "maintenance.connect_denied", en: "Maintenance connection denied", zh: "维护连接被拒绝" },
  { value: "maintenance.session_revoke", en: "Revoked maintenance session", zh: "撤销维护会话" },
  { value: "maintenance.command", en: "Maintenance command executed", zh: "维护命令已执行" },
  { value: "maintenance.command_denied", en: "Maintenance command denied", zh: "维护命令被拒绝" },
  { value: "maintenance.command_queued", en: "Maintenance write queued for approval", zh: "维护写命令待审批" },
  { value: "maintenance.approval_decide", en: "Maintenance approval decided", zh: "维护写审批决定" },
  { value: "maintenance.cleanup", en: "Maintenance cleanup ran", zh: "维护清理执行" },
  { value: "onboarding.complete", en: "Completed onboarding", zh: "完成首次部署向导" },
  { value: "onboarding.test_request", en: "Onboarding test request", zh: "向导测试请求" },
];

export const AUDIT_ACTION_VALUES: readonly string[] = AUDIT_ACTION_CATALOG.map((spec) => spec.value);

export const AUDIT_ACTION_LABELS: Readonly<Record<string, string>> = Object.fromEntries(
  AUDIT_ACTION_CATALOG.map((spec) => [spec.value, spec.en]),
);

export const AUDIT_ACTION_LABELS_ZH: Readonly<Record<string, string>> = Object.fromEntries(
  AUDIT_ACTION_CATALOG.map((spec) => [spec.value, spec.zh]),
);

export function isAuditAction(value: string): boolean {
  return AUDIT_ACTION_VALUES.includes(value);
}
