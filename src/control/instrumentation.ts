/**
 * FR-35：启动时做一次授权检查并记录状态。
 * DB 尚未迁移/未就绪时只告警，不阻止进程启动；控制面会在首个请求时再算一次。
 *
 * FR-11：模型目录的定时刷新在 lib/catalog-refresh.ts，只在 Node 运行时启动。
 */

export async function register() {
  if (process.env.NEXT_RUNTIME !== "nodejs") return;
  try {
    const { getLicenseStatus } = await import("./lib/license");
    const status = await getLicenseStatus(true);
    console.log(`license: status=${status.status} writable=${status.writable}`);
  } catch (error) {
    console.warn("license: startup check skipped", error instanceof Error ? error.message : error);
  }
  try {
    const mod = await import("./lib/catalog-refresh");
    mod.startCatalogRefresh();
  } catch (error) {
    console.warn("model catalog: scheduler unavailable", error instanceof Error ? error.message : error);
  }
}
