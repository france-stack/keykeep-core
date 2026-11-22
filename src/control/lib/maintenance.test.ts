import { describe, expect, it } from "vitest";

import {
  ALL_COMMAND_KEYS,
  MAINT_CRED_PREFIX,
  MAINT_AUDIT_ACTIONS,
  READONLY_COMMANDS,
  WRITE_COMMANDS,
  checkSession,
  checkTicket,
  commandAllowedByScope,
  credMatches,
  decideCommand,
  isCredFormat,
  issueMaintenanceCred,
  maintenanceEnabled,
} from "./maintenance";

const T0 = new Date("2026-11-16T00:00:00Z");
const hours = (n: number) => new Date(T0.getTime() + n * 3600_000);

function ticket(overrides: Partial<Parameters<typeof checkTicket>[0]> = {}) {
  return {
    approvedAt: hours(-1),
    expiresAt: hours(3),
    revokedAt: null,
    createdById: "customer-admin",
    ...overrides,
  };
}

describe("maintenanceEnabled (FR-60 / RM-A01)", () => {
  it("is off by default — nothing enables it except an explicit value", () => {
    expect(maintenanceEnabled({} as unknown as NodeJS.ProcessEnv)).toBe(false);
    expect(maintenanceEnabled({ KEYKEEP_MAINTENANCE_ENABLED: "" } as unknown as NodeJS.ProcessEnv)).toBe(false);
    expect(maintenanceEnabled({ KEYKEEP_MAINTENANCE_ENABLED: "0" } as unknown as NodeJS.ProcessEnv)).toBe(false);
    expect(maintenanceEnabled({ KEYKEEP_MAINTENANCE_ENABLED: "false" } as unknown as NodeJS.ProcessEnv)).toBe(false);
    expect(maintenanceEnabled({ KEYKEEP_MAINTENANCE_ENABLED: "yes" } as unknown as NodeJS.ProcessEnv)).toBe(false);
  });

  it("turns on only with explicit 1/true", () => {
    expect(maintenanceEnabled({ KEYKEEP_MAINTENANCE_ENABLED: "1" } as unknown as NodeJS.ProcessEnv)).toBe(true);
    expect(maintenanceEnabled({ KEYKEEP_MAINTENANCE_ENABLED: "true" } as unknown as NodeJS.ProcessEnv)).toBe(true);
    expect(maintenanceEnabled({ KEYKEEP_MAINTENANCE_ENABLED: "TRUE" } as unknown as NodeJS.ProcessEnv)).toBe(true);
  });
});

describe("checkTicket (FR-61 / RM-A02)", () => {
  it("accepts a live ticket owned by the customer admin", () => {
    expect(checkTicket(ticket(), T0, "customer-admin")).toEqual({ ok: true });
  });

  it("rejects a missing ticket", () => {
    expect(checkTicket(null, T0, "customer-admin")).toEqual({ ok: false, reason: "not_found" });
  });

  it("rejects an expired ticket", () => {
    expect(checkTicket(ticket({ expiresAt: hours(-0.5) }), T0, "customer-admin")).toEqual({
      ok: false,
      reason: "expired",
    });
  });

  it("rejects a foreign ticket (other customer's ticket)", () => {
    expect(checkTicket(ticket({ createdById: "someone-else" }), T0, "customer-admin")).toEqual({
      ok: false,
      reason: "foreign_ticket",
    });
  });

  it("rejects a revoked ticket", () => {
    expect(checkTicket(ticket({ revokedAt: T0 }), T0, "customer-admin")).toEqual({
      ok: false,
      reason: "revoked",
    });
  });

  it("rejects before the approval window opens", () => {
    expect(checkTicket(ticket({ approvedAt: hours(1) }), T0, "customer-admin")).toEqual({
      ok: false,
      reason: "window_not_open",
    });
  });
});

describe("short-lived credentials (FR-62)", () => {
  it("issues mk_ credentials whose plaintext never lands in storage shape", () => {
    const cred = issueMaintenanceCred();
    expect(cred.plaintext.startsWith(MAINT_CRED_PREFIX)).toBe(true);
    expect(isCredFormat(cred.plaintext)).toBe(true);
    expect(cred.plaintext).toHaveLength(46); // mk_ + 43
    expect(cred.credHash).toHaveLength(32);
    expect(cred.credHash.toString("utf8")).not.toContain(cred.plaintext);
  });

  it("hashes deterministically and matches only the right credential", () => {
    const cred = issueMaintenanceCred();
    const other = issueMaintenanceCred();
    expect(credMatches(cred.plaintext, cred.credHash)).toBe(true);
    expect(credMatches(other.plaintext, cred.credHash)).toBe(false);
    expect(credMatches("mk_invalid", cred.credHash)).toBe(false);
    expect(credMatches("rm -rf /", cred.credHash)).toBe(false);
  });

  it("generates distinct credentials", () => {
    const set = new Set(Array.from({ length: 50 }, () => issueMaintenanceCred().plaintext));
    expect(set.size).toBe(50);
  });
});

describe("checkSession (FR-62 / RM-A04)", () => {
  type SessionRow = Parameters<typeof checkSession>[0];
  function session(overrides: Partial<NonNullable<SessionRow>> = {}): NonNullable<SessionRow> {
    return {
      id: "session-1",
      expiresAt: hours(1),
      revokedAt: null,
      endedAt: null,
      commandScope: [],
      host: "127.0.0.1",
      account: "maint",
      ticket: ticket(),
      ...overrides,
    };
  }

  it("accepts a live session on a live ticket", () => {
    const result = checkSession(session(), T0, "customer-admin");
    expect(result).toMatchObject({ ok: true, sessionId: "session-1" });
  });

  it("rejects revoked/expired/ended sessions immediately", () => {
    expect(checkSession(session({ revokedAt: T0 }), T0, "customer-admin")).toMatchObject({
      ok: false,
      reason: "revoked",
    });
    expect(checkSession(session({ expiresAt: hours(-0.1) }), T0, "customer-admin")).toMatchObject({
      ok: false,
      reason: "expired",
    });
    expect(checkSession(session({ endedAt: hours(-0.1) }), T0, "customer-admin")).toMatchObject({
      ok: false,
      reason: "ended",
    });
  });

  it("rejects unknown credentials", () => {
    expect(checkSession(null, T0, "customer-admin")).toMatchObject({
      ok: false,
      reason: "unknown_credential",
    });
  });

  it("dies with the ticket: expired / revoked / foreign ticket invalidates the session", () => {
    expect(
      checkSession(session({ ticket: ticket({ expiresAt: hours(-0.5) }) }), T0, "customer-admin"),
    ).toMatchObject({ ok: false, reason: "ticket_expired" });
    expect(
      checkSession(session({ ticket: ticket({ revokedAt: T0 }) }), T0, "customer-admin"),
    ).toMatchObject({ ok: false, reason: "ticket_revoked" });
    expect(
      checkSession(session({ ticket: ticket({ createdById: "other" }) }), T0, "customer-admin"),
    ).toMatchObject({ ok: false, reason: "ticket_foreign_ticket" });
  });
});

describe("command policy (FR-67 / RM-A05)", () => {
  it("executes allowlisted readonly commands directly", () => {
    for (const key of Object.keys(READONLY_COMMANDS)) {
      expect(decideCommand(key)).toEqual({ verdict: "execute", commandKey: key, kind: "readonly" });
    }
  });

  it("queues allowlisted write commands for approval — never executes them", () => {
    for (const key of Object.keys(WRITE_COMMANDS)) {
      expect(decideCommand(key)).toEqual({ verdict: "queue_approval", commandKey: key, kind: "write" });
    }
  });

  it("rejects anything off the allowlist outright — no approval path", () => {
    expect(decideCommand("rm -rf /")).toEqual({ verdict: "reject", reason: "not_in_allowlist" });
    expect(decideCommand("cat /etc/passwd")).toEqual({ verdict: "reject", reason: "not_in_allowlist" });
    expect(decideCommand("diag.tables; cat /flag")).toEqual({ verdict: "reject", reason: "not_in_allowlist" });
    expect(decideCommand("")).toEqual({ verdict: "reject", reason: "not_in_allowlist" });
    expect(decideCommand("diag.nope")).toEqual({ verdict: "reject", reason: "unknown_command" });
  });

  it("enforces the session command scope", () => {
    expect(commandAllowedByScope("diag.tables", [])).toBe(true);
    expect(commandAllowedByScope("diag.tables", ["diag.tables"])).toBe(true);
    expect(commandAllowedByScope("diag.tables", ["diag.env"])).toBe(false);
  });

  it("readonly and write registries never overlap", () => {
    const readonlyKeys = new Set(Object.keys(READONLY_COMMANDS));
    for (const key of Object.keys(WRITE_COMMANDS)) {
      expect(readonlyKeys.has(key)).toBe(false);
    }
    expect(ALL_COMMAND_KEYS).toHaveLength(readonlyKeys.size + Object.keys(WRITE_COMMANDS).length);
  });
});

describe("audit actions (FR-64 / RM-A07)", () => {
  it("registers the five linkable categories with unique values", () => {
    const values = Object.values(MAINT_AUDIT_ACTIONS);
    expect(new Set(values).size).toBe(values.length);
    // 五类可关联：连接 / 审批 / 命令 / 结果 / 撤销
    expect(values).toContain("maintenance.session_connect");
    expect(values).toContain("maintenance.approval_decide");
    expect(values).toContain("maintenance.command");
    expect(values).toContain("maintenance.session_revoke");
    expect(values).toContain("maintenance.ticket_revoke");
  });
});
