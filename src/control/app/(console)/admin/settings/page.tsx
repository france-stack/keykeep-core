import { redirect } from "next/navigation";

/**
 * 旧书签兼容：设置页已迁出 /admin 前缀（canonical 在 /me/settings）。
 * 用 Next 内建 redirect（307，临时重定向）而不是 permanentRedirect，理由同 usage。
 *
 * 这里不需要额外声明 dynamic：父级 (console)/layout.tsx 会 await cookies()，
 * 整棵子树都是按请求动态渲染的，所以这条 redirect 在运行时结算，真的回 307。
 */
export default async function LegacySettingsRedirect() {
  redirect("/me/settings");
}
