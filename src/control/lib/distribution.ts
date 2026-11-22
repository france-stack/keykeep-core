// 「分发 API」的纯规则。
//
// 一个分发 = 老板定义的一个具名入口（例如「研发-DeepSeek」），带一组默认可用模型；
// 把它分配给员工时，落在员工名下的是虚拟 Key（网关只认 ApiKey，鉴权链路不变）。
//
// 两层的分工是刻意分开的：
//   - Distribution.allowedModels 是**默认值**：分配时预填到那把 Key 上；
//   - ApiKey.allowedModels 是**生效值**：网关白名单只读它（policy/whitelist.go），
//     所以「这个员工能用哪些模型」永远以 Key 上的名单为准。
// 分配时允许单独指定该员工的模型（需求②「选定该员工可用模型」）；
// 改分发默认值默认**不回刷**已有 Key（否则会悄悄改掉已交付给员工的权限），
// 要回刷必须在 PATCH 里显式传 syncModels: true。
//
// 这里保持零依赖（不碰 prisma / next）：路由与客户端都要用，且要能单测。

export const MAX_MODELS = 100;
export const MAX_KEYS_PER_ASSIGN = 100;
export const MAX_NAME = 64;
export const MAX_DESCRIPTION = 500;
export const MAX_LABEL = 64;

/**
 * 解析模型名单。接受数组（元素内也可带分隔符）或逗号/空白分隔的字符串；
 * 去重并保序，超过 MAX_MODELS 的部分截断（与该路由历史上的 slice(0,100) 一致）。
 */
export function normalizeAllowedModels(value: unknown): string[] {
  const raw: string[] = [];
  if (Array.isArray(value)) {
    for (const item of value) {
      if (typeof item === "string") raw.push(...item.split(/[,，\s]+/));
    }
  } else if (typeof value === "string") {
    raw.push(...value.split(/[,，\s]+/));
  }
  const out: string[] = [];
  const seen = new Set<string>();
  for (const item of raw) {
    const model = item.trim();
    if (!model || seen.has(model)) continue;
    seen.add(model);
    out.push(model);
    if (out.length >= MAX_MODELS) break;
  }
  return out;
}

/**
 * 分配时该用哪份名单：
 *   - body 里显式给了 allowedModels（含空数组，表示「该员工不限」）→ 用 body 的；
 *   - 没给 → 用分发上的默认名单。
 * 用 `hasOwnProperty` 而非真值判断，才能区分「没传」与「传了空数组」。
 */
export function resolveAssignmentModels(
  body: Record<string, unknown>,
  distributionModels: readonly string[],
): string[] {
  if (Object.prototype.hasOwnProperty.call(body, "allowedModels")) {
    return normalizeAllowedModels(body.allowedModels);
  }
  return [...distributionModels];
}

/** 分发名去空白后必须非空且不超长；返回 null 表示合法。 */
export function validateDistributionName(name: string): "invalid_name" | "name_too_long" | null {
  const trimmed = name.trim();
  if (!trimmed) return "invalid_name";
  if (trimmed.length > MAX_NAME) return "name_too_long";
  return null;
}

/**
 * 分发状态。撤销优先于停用：撤销是终态（其下 Key 全部 401），停用只是暂停继续分配。
 */
export function distributionStatus(distribution: {
  disabled: boolean;
  revokedAt: Date | string | null;
}): "active" | "disabled" | "revoked" {
  if (distribution.revokedAt) return "revoked";
  if (distribution.disabled) return "disabled";
  return "active";
}

/** 只有 active 的分发才能继续分配给员工。 */
export function canAssign(distribution: {
  disabled: boolean;
  revokedAt: Date | string | null;
}): boolean {
  return distributionStatus(distribution) === "active";
}

/** 分配数量夹到 1..MAX_KEYS_PER_ASSIGN；非法输入按 1 处理。 */
export function clampAssignCount(value: unknown): number {
  const n = Math.floor(Number(value ?? 1));
  if (!Number.isFinite(n) || n < 1) return 1;
  return Math.min(n, MAX_KEYS_PER_ASSIGN);
}

/** 展示用的模型名单：空数组 = 不限（与网关白名单语义一致）。 */
export function describeModels(models: readonly string[]): string {
  return models.length > 0 ? models.join(", ") : "不限";
}
