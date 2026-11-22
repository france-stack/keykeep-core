/**
 * PostgreSQL `::uuid` 的前置校验（C10）。
 *
 * 原生 SQL 把查询参数直接 cast 成 uuid 时，非法输入会让 PG 抛
 * `invalid input syntax for type uuid`，路由未捕获即 500。所有用户可控的
 * UUID 参数必须先过这里，非法一律 400，不进 SQL。
 *
 * 只接受规范 8-4-4-4-12 十六进制形状（大小写均可）：库里的 id 全部由
 * gen_random_uuid() 生成，必为该形状；PG 另接受的无连字符/花括号变体
 * 不是本系统的合法输入，一律拒绝。零 UUID（全局计数/无部门占位）合法。
 */
const UUID_RE = /^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$/i;

export function isUuid(value: string): boolean {
  return UUID_RE.test(value);
}