import type { ReactNode } from "react";

import AppShellClient from "./AppShellClient";

export type ShellSession = { account: string; displayName: string; role: string };

/** 控制台外壳（server）：读取运行时配置后交给客户端外壳。 */
export type ShellLicense = { status: string; message: string; writable: boolean };

export default function AppShell({
  session,
  license,
  children,
}: {
  session: ShellSession;
  license?: ShellLicense;
  children: ReactNode;
}) {
  const gatewayUrl = process.env.KEYKEEP_GATEWAY_URL ?? "http://127.0.0.1:8080";
  const version = process.env.KEYKEEP_VERSION ?? "0.1.0";
  return (
    <AppShellClient session={session} gatewayUrl={gatewayUrl} version={version} license={license}>
      {children}
    </AppShellClient>
  );
}
