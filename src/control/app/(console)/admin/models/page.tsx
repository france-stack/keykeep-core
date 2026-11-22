import { cookies } from "next/headers";
import { redirect } from "next/navigation";

import ModelsClient from "@/app/components/ModelsClient";
import { landingHref } from "@/app/components/nav";
import { SESSION_COOKIE, verifySession } from "@/lib/session";

export default async function ModelsPage() {
  const token = (await cookies()).get(SESSION_COOKIE)?.value;
  const session = token ? verifySession(token) : null;
  if (!session) redirect("/login");
  if (session.role !== "admin") redirect(landingHref(session.role));

  return <ModelsClient role={session.role} />;
}
