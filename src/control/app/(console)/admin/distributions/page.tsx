import { cookies } from "next/headers";
import { redirect } from "next/navigation";

import DistributionsClient from "@/app/components/DistributionsClient";
import { landingHref } from "@/app/components/nav";
import { SESSION_COOKIE, verifySession } from "@/lib/session";

export default async function DistributionsPage() {
  const token = (await cookies()).get(SESSION_COOKIE)?.value;
  const session = token ? verifySession(token) : null;
  if (!session) redirect("/login");
  if (session.role !== "admin") redirect(landingHref(session.role));

  return <DistributionsClient />;
}
