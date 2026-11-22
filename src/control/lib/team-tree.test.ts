import { describe, expect, it } from "vitest";

import {
  budgetPercent,
  buildTeamTree,
  memberCode,
  pulseCount,
  wirePath,
  type UsageAggregate,
  type UserInput,
} from "./team-tree";

function user(partial: Partial<UserInput> & { id: string; account: string }): UserInput {
  return {
    displayName: partial.displayName ?? partial.account,
    role: partial.role ?? "employee",
    disabled: partial.disabled ?? false,
    createdAt: partial.createdAt ?? "2026-01-01T00:00:00.000Z",
    ...partial,
  };
}

function usage(partial: Partial<UsageAggregate> & { empId: string }): UsageAggregate {
  return {
    calls: 0,
    errors: 0,
    costCents: 0,
    inputTokens: 0,
    outputTokens: 0,
    lastAt: null,
    ...partial,
  };
}

describe("buildTeamTree", () => {
  const users: UserInput[] = [
    user({ id: "e1", account: "linxiao", displayName: "Lin Xiao", createdAt: "2026-01-02T00:00:00.000Z" }),
    user({ id: "boss", account: "admin", displayName: "Boss", role: "admin", createdAt: "2026-01-03T00:00:00.000Z" }),
    user({ id: "e2", account: "zhao", displayName: "Zhao", createdAt: "2026-01-01T00:00:00.000Z" }),
    user({ id: "e3", account: "gone", displayName: "Gone", disabled: true, createdAt: "2026-01-04T00:00:00.000Z" }),
  ];

  it("老板取管理员，员工按创建时间升序，管理员不进员工列表", () => {
    const tree = buildTeamTree({ users, usage: [], budgets: [], keyCounts: {} });
    expect(tree.owner?.id).toBe("boss");
    expect(tree.employees.map((member) => member.id)).toEqual(["e2", "e1", "e3"]);
  });

  it("多个管理员时优先取启用的最早一位", () => {
    const tree = buildTeamTree({
      users: [
        user({ id: "old", account: "old", role: "admin", disabled: true, createdAt: "2025-01-01T00:00:00.000Z" }),
        user({ id: "new", account: "new", role: "admin", createdAt: "2026-02-01T00:00:00.000Z" }),
      ],
      usage: [],
      budgets: [],
      keyCounts: {},
    });
    expect(tree.owner?.id).toBe("new");
  });

  it("一个启用的管理员都没有时仍保留树根", () => {
    const tree = buildTeamTree({
      users: [user({ id: "only", account: "only", role: "admin", disabled: true })],
      usage: [],
      budgets: [],
      keyCounts: {},
    });
    expect(tree.owner?.id).toBe("only");
    expect(tree.employees).toHaveLength(0);
  });

  it("用量、预算与 Key 数按员工归位", () => {
    const tree = buildTeamTree({
      users,
      usage: [usage({ empId: "e1", calls: 12, errors: 1, costCents: 340, inputTokens: 900, outputTokens: 100 })],
      budgets: [
        { scope: "emp", scopeId: "e1", limitCents: 10_000, usedCents: 4_000 },
        { scope: "global", scopeId: "global", limitCents: 100_000, usedCents: 20_000 },
      ],
      keyCounts: { e1: 2 },
    });
    const member = tree.employees.find((item) => item.id === "e1");
    expect(member).toMatchObject({ calls: 12, errors: 1, costCents: 340, keyCount: 2 });
    expect(member?.budget).toEqual({ limitCents: 10_000, usedCents: 4_000 });
    expect(tree.globalBudget).toEqual({ limitCents: 100_000, usedCents: 20_000 });
    expect(tree.totals).toMatchObject({ members: 3, active: 1, calls: 12, costCents: 340 });
  });

  it("没有管理员时 owner 为 null，员工照常列出", () => {
    const tree = buildTeamTree({
      users: [user({ id: "e1", account: "linxiao" })],
      usage: [],
      budgets: [],
      keyCounts: {},
    });
    expect(tree.owner).toBeNull();
    expect(tree.employees).toHaveLength(1);
  });
});

describe("wirePath", () => {
  it("右侧员工走三折线，拐角带圆角", () => {
    const d = wirePath({ ox: 100, oy: 60, ex: 260, ey: 160, busY: 110 });
    expect(d).toContain("M 100 60");
    expect(d).toContain("Q 100 110 108 110");
    expect(d).toContain("H 252");
    expect(d).toContain("Q 260 110 260 118");
    expect(d).toContain("V 160");
  });

  it("左侧员工方向相反", () => {
    const d = wirePath({ ox: 300, oy: 60, ex: 120, ey: 160, busY: 110 });
    expect(d).toContain("Q 300 110 292 110");
    expect(d).toContain("H 128");
    expect(d).toContain("Q 120 110 120 118");
  });

  it("几乎垂直时退化成竖线，避免小钩子", () => {
    const d = wirePath({ ox: 200, oy: 60, ex: 205, ey: 160, busY: 110 });
    expect(d).toBe("M 200 60 V 160");
  });

  it("主干不会越过员工卡片顶部，且末端留出垂线", () => {
    // 员工卡片贴得很近时 busY 比 ey 大：拐角必须压在 ey 之上，且横向那段
    // 不能贴着卡片顶边穿进去（否则圆角被吃掉、看起来像一条直线）。
    const d = wirePath({ ox: 100, oy: 60, ex: 300, ey: 90, busY: 200 });
    const cornerY = Number(/V (\d+(?:\.\d+)?) Q/.exec(d)?.[1]);
    expect(cornerY).toBeLessThan(90);
    expect(cornerY).toBeGreaterThanOrEqual(60);
    // 末端仍回到员工卡片顶边。
    expect(d.trimEnd().endsWith("V 90")).toBe(true);
    // 没有回头路：x 方向只走一次横向。
    expect(d.match(/H /g)).toHaveLength(1);
  });
});

describe("小的展示换算", () => {
  it("预算水位夹在 0–100", () => {
    expect(budgetPercent(null)).toBe(0);
    expect(budgetPercent({ limitCents: 0, usedCents: 10 })).toBe(0);
    expect(budgetPercent({ limitCents: 1000, usedCents: 250 })).toBe(25);
    expect(budgetPercent({ limitCents: 1000, usedCents: 5000 })).toBe(100);
  });

  it("数据包数量按调用量分档，0 表示不画", () => {
    expect(pulseCount(0)).toBe(0);
    expect(pulseCount(1)).toBe(1);
    expect(pulseCount(9)).toBe(1);
    expect(pulseCount(10)).toBe(2);
    expect(pulseCount(99)).toBe(2);
    expect(pulseCount(100)).toBe(3);
    expect(pulseCount(99_999)).toBe(3);
  });

  it("编号优先用 account，超长截断；没有 account 用 id 前 8 位", () => {
    expect(memberCode({ account: "linxiao", id: "abcdefgh-1234" })).toBe("linxiao");
    expect(memberCode({ account: "a".repeat(30), id: "x" })).toBe(`${"a".repeat(17)}…`);
    expect(memberCode({ account: "", id: "abcdefgh-1234" })).toBe("abcdefgh");
  });
});
