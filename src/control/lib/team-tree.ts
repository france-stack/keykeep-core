/**
 * 主页面「老板 → 员工」拓扑树的纯函数部分。
 *
 * 拆成纯函数是为了两件事：树怎么搭、线怎么画，都能在没有 DOM 的情况下单测；
 * 组件只负责测量位置与渲染。几何约定见 wirePath 的注释。
 */

export type TeamMember = {
  id: string;
  account: string;
  displayName: string;
  role: string;
  disabled: boolean;
  createdAt: string;
  keyCount: number;
  calls: number;
  errors: number;
  costCents: number;
  inputTokens: number;
  outputTokens: number;
  /** 最近一次请求时间；从未调用过为 null */
  lastAt: string | null;
  /** 员工预算；没配为 null */
  budget: { limitCents: number; usedCents: number } | null;
};

export type TeamTotals = {
  members: number;
  active: number;
  calls: number;
  errors: number;
  costCents: number;
  inputTokens: number;
  outputTokens: number;
};

export type TeamTree = {
  owner: TeamMember | null;
  employees: TeamMember[];
  totals: TeamTotals;
  /** 全局预算；没配为 null */
  globalBudget: { limitCents: number; usedCents: number } | null;
};

/** usage 聚合行的最小形状（API 从 SQL 拿到的就是这些字段）。 */
export type UsageAggregate = {
  empId: string;
  calls: number;
  errors: number;
  costCents: number;
  inputTokens: number;
  outputTokens: number;
  lastAt: string | null;
};

export type UserInput = {
  id: string;
  account: string;
  displayName: string;
  role: string;
  disabled: boolean;
  createdAt: Date | string;
};

export type BudgetInput = { scope: string; scopeId: string; limitCents: number; usedCents: number };

const EMPTY_USAGE: UsageAggregate = {
  empId: "",
  calls: 0,
  errors: 0,
  costCents: 0,
  inputTokens: 0,
  outputTokens: 0,
  lastAt: null,
};

function toMember(
  user: UserInput,
  usage: UsageAggregate | undefined,
  budget: BudgetInput | undefined,
  keyCount: number,
): TeamMember {
  const stats = usage ?? EMPTY_USAGE;
  return {
    id: user.id,
    account: user.account,
    displayName: user.displayName,
    role: user.role,
    disabled: user.disabled,
    createdAt: typeof user.createdAt === "string" ? user.createdAt : user.createdAt.toISOString(),
    keyCount,
    calls: stats.calls,
    errors: stats.errors,
    costCents: stats.costCents,
    inputTokens: stats.inputTokens,
    outputTokens: stats.outputTokens,
    lastAt: stats.lastAt,
    budget: budget ? { limitCents: budget.limitCents, usedCents: budget.usedCents } : null,
  };
}

/**
 * 搭树。
 *
 * 「老板」取最早的启用管理员；一个都没有时退到最早的管理员（含已停用），
 * 那样也不会因为全部停用而丢掉树根。员工按创建时间升序，与 People 页一致。
 */
export function buildTeamTree(input: {
  users: UserInput[];
  usage: UsageAggregate[];
  budgets: BudgetInput[];
  keyCounts: Record<string, number>;
}): TeamTree {
  const { users, usage, budgets, keyCounts } = input;

  const usageByEmp = new Map(usage.map((row) => [row.empId, row]));
  const budgetByEmp = new Map(
    budgets.filter((row) => row.scope === "emp").map((row) => [row.scopeId, row]),
  );
  const globalBudget = budgets.find((row) => row.scope === "global") ?? null;

  const admins = users
    .filter((user) => user.role === "admin")
    .sort((a, b) => time(a.createdAt) - time(b.createdAt));
  const owner = admins.find((user) => !user.disabled) ?? admins[0] ?? null;

  const employees = users
    .filter((user) => user.role !== "admin" && user.id !== owner?.id)
    .sort((a, b) => time(a.createdAt) - time(b.createdAt))
    .map((user) =>
      toMember(user, usageByEmp.get(user.id), budgetByEmp.get(user.id), keyCounts[user.id] ?? 0),
    );

  const totals: TeamTotals = {
    members: employees.length,
    active: employees.filter((member) => member.calls > 0).length,
    calls: 0,
    errors: 0,
    costCents: 0,
    inputTokens: 0,
    outputTokens: 0,
  };
  for (const member of employees) {
    totals.calls += member.calls;
    totals.errors += member.errors;
    totals.costCents += member.costCents;
    totals.inputTokens += member.inputTokens;
    totals.outputTokens += member.outputTokens;
  }

  return {
    owner: owner
      ? toMember(
          owner,
          usageByEmp.get(owner.id),
          budgetByEmp.get(owner.id),
          keyCounts[owner.id] ?? 0,
        )
      : null,
    employees,
    totals,
    globalBudget: globalBudget
      ? { limitCents: globalBudget.limitCents, usedCents: globalBudget.usedCents }
      : null,
  };
}

function time(value: Date | string): number {
  const date = value instanceof Date ? value : new Date(value);
  const stamp = date.getTime();
  return Number.isNaN(stamp) ? 0 : stamp;
}

/** 预算水位：0–100 的整数百分比；没配预算时返回 0。 */
export function budgetPercent(budget: { limitCents: number; usedCents: number } | null): number {
  if (!budget || budget.limitCents <= 0) return 0;
  return Math.min(100, Math.max(0, Math.round((budget.usedCents / budget.limitCents) * 100)));
}

/**
 * 一条连线：老板卡片底边中点 → 横向主干 → 员工卡片顶边中点。
 *
 * 走的是制图常见的「三折线」，拐角用 8px 圆角，避免直角生硬：
 *
 *      ox,oy
 *        │
 *        └──────────┐        busY
 *                   │
 *                 ex,ey
 *
 * 两点水平距离小于两倍圆角时退化成一条竖线，否则会出现方向反转的小钩子。
 */
export function wirePath(input: { ox: number; oy: number; ex: number; ey: number; busY: number }): string {
  const { ox, oy, ex, ey, busY } = input;
  const dx = ex - ox;
  // 主干必须落在两端之间：超出时压回去，否则路径会先冲出卡片再折回。
  // 同时给末端留一小段垂线，避免横向那段贴着卡片顶边穿进去。
  const tail = 4;
  let cornerY = Math.min(Math.max(busY, oy), ey);
  if (ey - cornerY < tail && ey - oy >= tail) cornerY = ey - tail;
  const up = Math.max(0, cornerY - oy);
  const down = Math.max(0, ey - cornerY);
  const r = Math.min(8, up / 2, down / 2);

  if (Math.abs(dx) < r * 2) return `M ${ox} ${oy} V ${ey}`;

  const dir = dx > 0 ? 1 : -1;
  return [
    `M ${ox} ${oy}`,
    `V ${cornerY - r}`,
    `Q ${ox} ${cornerY} ${ox + dir * r} ${cornerY}`,
    `H ${ex - dir * r}`,
    `Q ${ex} ${cornerY} ${ex} ${cornerY + r}`,
    `V ${ey}`,
  ].join(" ");
}

/**
 * 一位员工该亮几颗「数据包」：按调用量分档，0 表示今天没有流量（线画成虚线且不动）。
 * 上限 3，避免热员工把页面画满。
 */
export function pulseCount(calls: number): number {
  if (calls <= 0) return 0;
  if (calls < 10) return 1;
  if (calls < 100) return 2;
  return 3;
}

/** 员工编号展示用：account 优先，过长就截短；没有 account 退回 id 前 8 位。 */
export function memberCode(member: Pick<TeamMember, "account" | "id">): string {
  const account = member.account?.trim();
  if (account) return account.length > 18 ? `${account.slice(0, 17)}…` : account;
  return member.id.slice(0, 8);
}
