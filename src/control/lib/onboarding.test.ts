import { describe, expect, it } from "vitest";

import {
  SEED_TIMESTAMP_TOLERANCE_MS,
  computeStep,
  isAdminPasswordChanged,
  onboardingComplete,
} from "./onboarding";

describe("onboarding 判定", () => {
  const T0 = new Date("2026-11-16T10:00:00Z");

  it("seed 同刻写入口令时间戳不算改密", () => {
    expect(isAdminPasswordChanged({ passwordChangedAt: T0, createdAt: T0 })).toBe(false);
  });

  it("容差窗口内的口令时间戳不算改密", () => {
    expect(
      isAdminPasswordChanged({
        passwordChangedAt: new Date(T0.getTime() + SEED_TIMESTAMP_TOLERANCE_MS),
        createdAt: T0,
      }),
    ).toBe(false);
  });

  it("真实改密（晚于建号超容差）判为已改", () => {
    expect(
      isAdminPasswordChanged({
        passwordChangedAt: new Date(T0.getTime() + SEED_TIMESTAMP_TOLERANCE_MS + 1),
        createdAt: T0,
      }),
    ).toBe(true);
  });

  it("passwordChangedAt 为空 = 未改密", () => {
    expect(isAdminPasswordChanged({ passwordChangedAt: null, createdAt: T0 })).toBe(false);
  });

  it("早于建号的口令时间戳（时钟回拨数据）不算改密", () => {
    expect(
      isAdminPasswordChanged({ passwordChangedAt: new Date(T0.getTime() - 60_000), createdAt: T0 }),
    ).toBe(false);
  });

  describe("computeStep", () => {
    it("未改密 → 第 1 步", () => {
      expect(computeStep({ passwordChanged: false, hasRoutableProvider: false })).toBe(1);
      expect(computeStep({ passwordChanged: false, hasRoutableProvider: true })).toBe(1);
    });

    it("已改密、无供应商 → 第 2 步", () => {
      expect(computeStep({ passwordChanged: true, hasRoutableProvider: false })).toBe(2);
    });

    it("已改密、有供应商 → 第 3 步", () => {
      expect(computeStep({ passwordChanged: true, hasRoutableProvider: true })).toBe(3);
    });
  });

  describe("onboardingComplete", () => {
    it("三要素齐备才完成", () => {
      expect(onboardingComplete({ passwordChanged: true, hasRoutableProvider: true, skippedProvider: false })).toBe(true);
      expect(onboardingComplete({ passwordChanged: true, hasRoutableProvider: false, skippedProvider: true })).toBe(true);
      expect(onboardingComplete({ passwordChanged: true, hasRoutableProvider: true, skippedProvider: true })).toBe(true);
      expect(onboardingComplete({ passwordChanged: false, hasRoutableProvider: true, skippedProvider: true })).toBe(false);
      expect(onboardingComplete({ passwordChanged: true, hasRoutableProvider: false, skippedProvider: false })).toBe(false);
    });

    it("跳过分支必须能结束向导（否则每次刷新都被拉回第 2 步）", () => {
      // 桌面首装的真实形态：有内置演示上游（provider='mock'，不算 hasRoutableProvider），
      // 用户改了密并在第 2 步点了跳过。
      const skipped = { passwordChanged: true, hasRoutableProvider: false, skippedProvider: true };
      expect(onboardingComplete(skipped)).toBe(true);
      // 同一组状态算 needed 时必须是 false —— 这正是 proxy 判断要不要重定向的口径。
      expect(onboardingComplete(skipped)).not.toBe(false);
    });
  });
});
