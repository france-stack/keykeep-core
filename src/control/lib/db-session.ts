// 数据库会话的连接串规整：会话时区固定为 UTC。
//
// 为什么必须这样做（2026-11-08 抓包确认，不是理论风险）：
//
// @prisma/adapter-pg 6.19.3 的 formatDateTime() 只拼 UTC 墙钟、**不带任何时区
// 后缀**。线上实际下发的参数是 `2026-11-08 06:36:23.302`——PostgreSQL 把这样一个
// 裸 timestamp 按**会话时区**解释，于是服务器 timezone=Asia/Shanghai 时，
// 写进库的瞬间就被减了 8 小时。
//
// 读回来时 normalize_timestamptz() 把 `+08` 后缀改写成 `+00:00` 却不动钟面，
// 于是每个时间读出来又晚 8 小时。两个偏移在「控制面写入 + 控制面读取」这条闭环里
// 正好抵消，所以界面看着正常；受害的是网关侧写入的行（pgx 直连、时间正确）——
// 审计页把它们显示成 +8 小时，它们进哈希链也对不上控制面的复算。
// 容器版 PG 默认 UTC 因而侥幸不触发，桌面版内置 PG 的 timezone=Asia/Shanghai 每次都触发。
//
// 会话时区改成 UTC 之后，写入与读取都落在真实瞬间；因此这一处既修显示，
// 也修「摘要能不能复算」。
//
// 容易看错的两点（都实测过，别照直觉改）：
//   * Prisma 自己的查询日志会把参数打印成 `... UTC`，那是日志格式，不是线上值；
//   * 显式写 `... UTC` 后缀给 PostgreSQL 是**正确**的（epoch 与 `+00` 完全一致），
//     错的只有"没有后缀"这一种形态。
//
// 运行时实测：会话 Asia/Shanghai 时写偏移 −8.0h、读偏移 +8.0h；UTC 时两者 0.0h。
export const TIMEZONE_OPTION = "-c timezone=UTC";

/**
 * 在连接串上追加会话选项；已经带 options= 的原样返回。
 * `.mjs` 版本供 seed / 一次性脚本使用，两处共用同一份字符串常量。
 */
export function withUtcSession(url: string): string {
  if (!url || url.includes("options=")) return url;
  const separator = url.includes("?") ? "&" : "?";
  return `${url}${separator}options=${encodeURIComponent(TIMEZONE_OPTION)}`;
}
