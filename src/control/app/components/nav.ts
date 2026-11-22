import type { IconName } from "./Icon";

export type Role = "admin" | "employee";

export type NavItem = {
  id: string;
  label: string;
  /** 没有 href 表示占位项（disabled） */
  href?: string;
  icon: IconName;
  roles?: Role[];
  disabled?: boolean;
  tooltip?: string;
  /** 额外高亮路径，例如价格版本归属模型目录 */
  aliases?: string[];
  /**
   * 只匹配自身，不再按前缀吞掉子路径。
   * 用于「父页面本身不是目录」的情况：`/me`（我的接口）下面又挂了 `/me/usage`、
   * `/me/settings` 两个独立页面，若按前缀匹配，面包屑会全变成「我的接口」、
   * 侧栏也会同时点亮两项。
   */
  exact?: boolean;
};

export type NavGroup = {
  id: string;
  /** 分组标题；不填表示这组不显示标题（首组按角色的入口不再有「工作台」字样）。 */
  label?: string;
  items: NavItem[];
};

const ALL: Role[] = ["admin", "employee"];
const STAFF: Role[] = ["admin"];

// 分组与命名对齐 Magpie 0.1.2145：模型 / 观测 / 扩展。
// 老板与员工看的是两套菜单：老板管「人与上游」，员工只需要「我的接口 + 我用了多少」。
// 首组不放分组标题：登录后直接就是自己的入口，不需要「工作台」这类层级名。
export const NAV_GROUPS: NavGroup[] = [
  {
    id: "work",
    items: [
      // 员工侧唯一的「操作」入口：拿到 Base URL 与 Key，抄进 Claude Code / DeepSeek 客户端。
      { id: "my-endpoint", label: "我的接口", href: "/me", icon: "key", roles: ["employee"], exact: true },
      { id: "keys", label: "API 密钥", href: "/admin/keys", icon: "key", roles: STAFF },
      // 老板先把「能用哪些模型」配成一个分发 API，再按员工发出去。
      // 排在 /admin/keys 之后：登录落点保持 /admin/keys，不改变既有的首屏与脚本约定。
      { id: "distributions", label: "分发 API", href: "/admin/distributions", icon: "share", roles: STAFF },
      { id: "org", label: "成员", href: "/admin/org", icon: "users", roles: STAFF },
    ],
  },
  {
    id: "models",
    label: "模型",
    items: [
      { id: "providers", label: "供应商", href: "/admin/providers", icon: "plug", roles: ["admin"] },
      {
        id: "catalog",
        label: "模型与价格",
        href: "/admin/models",
        icon: "cube",
        roles: STAFF,
        aliases: ["/admin/prices"],
      },
      { id: "limits", label: "全局限流", href: "/admin/limits", icon: "activity", roles: ["admin"] },
    ],
  },
  {
    id: "insights",
    label: "观测",
    items: [
      // 用量与设置两个角色共用，所以 canonical 放在 /me/*，不占老板的 /admin 前缀；
      // 旧路径留在 aliases 里，只为让老书签/深链进来时高亮与面包屑仍然正确。
      {
        id: "usage",
        label: "用量与请求",
        href: "/me/usage",
        icon: "chart",
        roles: ALL,
        aliases: ["/admin/usage"],
      },
      { id: "budgets", label: "预算", href: "/admin/budgets", icon: "wallet", roles: ["admin"] },
      { id: "audit", label: "审计日志", href: "/admin/audit", icon: "shield", roles: ["admin"] },
    ],
  },
  {
    id: "extend",
    label: "扩展",
    items: [
      { id: "redaction", label: "脱敏", href: "/admin/redaction", icon: "shield", roles: ["admin"] },
      {
        id: "maintenance",
        label: "维护通道",
        href: "/admin/maintenance",
        icon: "wrench",
        // 员工看不到这个入口：维护平面是老板与厂商之间的事。
        roles: STAFF,
      },
    ],
  },
];

export const SETTINGS_ITEM: NavItem = {
  id: "settings",
  label: "设置",
  href: "/me/settings",
  icon: "gear",
  roles: ALL,
  aliases: ["/admin/settings"],
};

export function canSeeItem(role: string, item: NavItem): boolean {
  if (!item.roles) return true;
  return (item.roles as string[]).includes(role);
}

export function visibleGroups(role: string): NavGroup[] {
  return NAV_GROUPS.map((group) => ({
    ...group,
    items: group.items.filter((item) => canSeeItem(role, item)),
  })).filter((group) => group.items.length > 0);
}

/**
 * 登录后的落点：该角色能看见的第一个导航项。
 * 删掉「总览」后 "/" 不再是一个页面，所有「回首页」的跳转都必须走这里，
 * 否则员工会被丢到老板的密钥页、老板会被丢到 404。
 */
export function landingHref(role: string): string {
  for (const group of visibleGroups(role)) {
    for (const item of group.items) {
      if (item.href && !item.disabled) return item.href;
    }
  }
  // 理论上不可达（设置项对两个角色都可见），兜底到设置页而不是一个空路由。
  return SETTINGS_ITEM.href as string;
}

export function isActive(pathname: string, item: NavItem): boolean {
  if (!item.href) return false;
  if (pathname === item.href) return true;
  if (item.exact) return (item.aliases ?? []).some((alias) => pathname === alias || pathname.startsWith(alias + "/"));
  if (pathname.startsWith(item.href + "/")) return true;
  return (item.aliases ?? []).some((alias) => pathname === alias || pathname.startsWith(alias + "/"));
}

/** 角色中文名：产品只有老板（admin）与员工（employee）。 */
export function roleLabel(role: string): string {
  if (role === "admin") return "老板";
  if (role === "employee") return "员工";
  return role;
}

/** 不在导航里的页面（面包屑仍需要一个标题）。 */
const EXTRA_LABELS: Record<string, string> = {
  "/license": "授权",
  // FR-01 向导不占菜单：完成后不该出现在侧栏里，但面包屑要有名字。
  "/admin/onboarding": "首次部署向导",
};

export function pageMeta(pathname: string): { group?: string; label: string; item?: NavItem } {
  const groups = NAV_GROUPS.map((group) => ({
    ...group,
    items: group.items.filter((item) => item.href),
  }));
  for (const group of groups) {
    for (const item of group.items) {
      if (isActive(pathname, item)) {
        // 首组没有分组标题，面包屑只显示当前页名。
        return { group: group.label, label: item.label, item };
      }
    }
  }
  if (isActive(pathname, SETTINGS_ITEM)) {
    return { label: SETTINGS_ITEM.label, item: SETTINGS_ITEM };
  }
  // 兜底不再写「控制台」：那既不是页面名，也会在面包屑里凭空多出一层。
  return { label: EXTRA_LABELS[pathname] ?? "KeyKeep" };
}
