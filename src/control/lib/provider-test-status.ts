/**
 * 供应商「测试」按钮的行内状态描述（F4）。
 *
 * 改造前结果只渲染在展开的详情区里：详情折叠时点「测试」，按钮变成
 * 「测试中…」后又恢复，用户看不到任何结论。这里把「该显示什么」抽成
 * 纯函数，行内状态位与详情区共用同一份判定，避免两处文案漂移。
 */

export type ProviderTestResult = {
  ok: boolean;
  status: number | null;
  latency_ms: number;
  error?: string;
};

export type ProviderTestStatus = {
  /** 行内状态文本；空串表示不显示状态位。 */
  label: string;
  /** 复用 .status 的修饰类：success / error / limited / 空。 */
  tone: "" | "success" | "error" | "limited";
  /** 悬停提示：成功给耗时，失败给错误码。 */
  title: string;
  /** 是否还在进行中（按钮据此禁用）。 */
  testing: boolean;
};

export function describeTestStatus(result: ProviderTestResult | undefined): ProviderTestStatus {
  if (!result) {
    return { label: "", tone: "", title: "", testing: false };
  }
  if (result.error === "testing") {
    return { label: "测试中…", tone: "limited", title: "正在向该供应商发起一次探测请求", testing: true };
  }
  if (result.ok) {
    const latency = Number.isFinite(result.latency_ms) ? Math.max(0, Math.round(result.latency_ms)) : 0;
    return {
      label: `连接成功 ${latency} ms`,
      tone: "success",
      title: `连接成功，耗时 ${latency} ms`,
      testing: false,
    };
  }
  const reason = result.error ?? "connection_failed";
  return {
    label: "连接失败",
    tone: "error",
    title: `连接失败：${reason}`,
    testing: false,
  };
}
