/**
 * 授权验签公钥。
 *
 * 生产发布时把 v1 替换为实际公钥（SPKI DER 的 base64）；运行期不接受远程下发。
 * 非生产环境允许用 KEYKEEP_LICENSE_PUBLIC_KEY 注入测试公钥，便于本地/验收；
 * 生产环境忽略该环境变量，避免部署配置成为第二信任源。
 */
const EMBEDDED: Record<string, string> = {
  v1: "",
};

export function publicKeyFor(kid: string): string | null {
  if (process.env.NODE_ENV !== "production") {
    const override = process.env.KEYKEEP_LICENSE_PUBLIC_KEY;
    if (override && override.trim()) return override.trim();
  }
  const embedded = EMBEDDED[kid];
  return embedded && embedded.trim() ? embedded.trim() : null;
}

export function isDevPublicKeyOverrideAllowed(): boolean {
  return process.env.NODE_ENV !== "production";
}
