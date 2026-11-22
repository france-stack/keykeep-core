export const SIDEBAR_DEFAULT = 212;
export const SIDEBAR_MIN = 168;
export const SIDEBAR_MAX = 320;
export const SIDEBAR_KEY = "kk-sidebar-w";

export function clampSidebarWidth(value: number): number {
  if (!Number.isFinite(value)) return SIDEBAR_DEFAULT;
  return Math.min(SIDEBAR_MAX, Math.max(SIDEBAR_MIN, Math.round(value)));
}

export function parseStoredWidth(raw: string | null | undefined): number {
  if (!raw) return SIDEBAR_DEFAULT;
  const parsed = Number.parseInt(raw, 10);
  return Number.isFinite(parsed) ? clampSidebarWidth(parsed) : SIDEBAR_DEFAULT;
}
