// 数据库会话的连接串规整：会话时区固定为 UTC。
//
// 为什么必须这样做（2026-11-08 实测确认，不是理论风险）：
//
// @prisma/adapter-pg 的 formatDateTime() 只拼 UTC 墙钟、**不带时区后缀**：线上
// 实际下发的是 `2026-11-08 06:36:23.302`。PostgreSQL 把裸 timestamp 按**会话
// 时区**解释，服务器 timezone=Asia/Shanghai 时写入就被减了 8 小时；读回来时
// normalize_timestamptz 又把 `+08` 后缀改写成 `+00:00` 而不动钟面，于是每个
// 时间读出来又晚 8 小时。两个偏移在"控制面写入 + 控制面读取"这条闭环里正好
// 抵消，所以界面看着是对的；受害的是网关侧写入的行（pgx 直连、时间正确）——
// 它们被显示成 +8 小时，进哈希链也对不上。
//
// 注意：Prisma 查询日志里那句 `... UTC` 是日志格式，不是线上值；给 PostgreSQL
// 显式写 `... UTC` 后缀反而是正确的。错的只有"没有后缀"这一种形态。
//
// 会话时区改成 UTC 之后，写入与读取都落在真实瞬间。这是唯一一处能在
// 不改 Prisma/adapter 版本的前提下同时修好两侧的办法。
//
// 这个文件是 .mjs 而不是 .ts：seed / migrate 这类一次性脚本由 node 直接跑，
// 不走 TypeScript 编译。

export const TIMEZONE_OPTION = "-c timezone=UTC";

/**
 * 在连接串上追加会话选项；已经带 options= 的原样返回。
 * @param {string} url
 * @returns {string}
 */
export function withUtcSession(url) {
  if (!url || url.includes("options=")) return url;
  const separator = url.includes("?") ? "&" : "?";
  return `${url}${separator}options=${encodeURIComponent(TIMEZONE_OPTION)}`;
}
