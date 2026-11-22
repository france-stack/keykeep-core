import { cookies } from "next/headers";
import { redirect } from "next/navigation";

import RedactionClient from "@/app/components/RedactionClient";
import { landingHref } from "@/app/components/nav";
import { SESSION_COOKIE, verifySession } from "@/lib/session";

export default async function RedactionPage() {
  const token = (await cookies()).get(SESSION_COOKIE)?.value;
  const session = token ? verifySession(token) : null;
  if (!session) redirect("/login");
  if (session.role !== "admin") redirect(landingHref(session.role));

  return (
    <div className="page">
      <div className="page-head">
        <div>
          <h1 className="page-title">脱敏</h1>
          <p className="page-sub">在请求到达上游模型之前，把密钥与个人敏感信息按规则打码。</p>
        </div>
      </div>
      <RedactionClient mode={process.env.KEYKEEP_MASK_MODE ?? "rules-only"} />
    </div>
  );
}
