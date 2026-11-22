import { cookies } from "next/headers";
import { redirect } from "next/navigation";

import LicenseClient from "@/app/components/LicenseClient";
import { landingHref } from "@/app/components/nav";
import { getLicenseStatus } from "@/lib/license";
import { SESSION_COOKIE, verifySession } from "@/lib/session";

export default async function LicensePage() {
  const token = (await cookies()).get(SESSION_COOKIE)?.value;
  const session = token ? verifySession(token) : null;
  if (!session) redirect("/login");
  // 授权页与其他管理页同级的 admin 门禁：员工不该看到完整机器码与授权 ID。
  if (session.role !== "admin") redirect(landingHref(session.role));

  const status = await getLicenseStatus(true);
  return (
    <div className="page">
      <div className="page-head">
        <div>
          <h1 className="page-title">授权</h1>
          <p className="page-sub">离线 Ed25519 授权、机器绑定、试用期与吊销。</p>
        </div>
      </div>
      <LicenseClient status={status} role={session.role} />
    </div>
  );
}
