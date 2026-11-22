/**
 * FR-11 模型目录的后台刷新。只在 Node 运行时由 instrumentation.ts 启动。
 * 本模块不碰文件系统：目录与价格都落在数据库里，拉取失败保留旧数据。
 */
import { recordAudit } from "./audit";
import { syncModelCatalog } from "./model-catalog";

let timer: ReturnType<typeof setInterval> | null = null;

async function refresh(): Promise<void> {
  try {
    const summary = await syncModelCatalog();
    console.log(
      `model catalog: ${summary.models} models from ${summary.providers} providers (version ${summary.version})`,
    );
  } catch (error) {
    const reason = error instanceof Error ? error.message : String(error);
    console.warn("model catalog: refresh failed", reason);
    try {
      await recordAudit({ action: "model.catalog.sync_failed", detail: { reason: reason.slice(0, 200) } });
    } catch {
      // 审计失败不再向上抛：目录刷新是后台任务。
    }
  }
}

/** 启动后先跑一次，之后按 MODEL_CATALOG_REFRESH_HOURS（默认 24，0 关闭）刷新。 */
export function startCatalogRefresh(): void {
  if (timer) return;
  const hours = Number(process.env.MODEL_CATALOG_REFRESH_HOURS ?? "24");
  if (!Number.isFinite(hours) || hours <= 0) {
    console.log("model catalog: periodic refresh disabled");
    return;
  }
  void refresh();
  timer = setInterval(() => void refresh(), Math.max(hours, 1) * 60 * 60 * 1000);
  timer.unref?.();
}
