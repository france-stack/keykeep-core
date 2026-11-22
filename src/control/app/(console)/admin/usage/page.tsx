import { redirect } from "next/navigation";

/**
 * 旧书签兼容：员工页已迁出 /admin 前缀（canonical 在 /me/usage）。
 * 用 Next 内建 redirect（307，临时重定向）而不是 permanentRedirect：
 * 这是产品口径调整，不是永久不变的地址，之后再迁移时不该被浏览器和 CDN 长期缓存住。
 * tab 参数要一并带过去，否则老书签里的「请求明细」会静默回到概览。
 */
export default async function LegacyUsageRedirect({
  searchParams,
}: {
  searchParams: Promise<{ tab?: string }>;
}) {
  const tab = (await searchParams).tab;
  redirect(tab === "requests" ? "/me/usage?tab=requests" : "/me/usage");
}
