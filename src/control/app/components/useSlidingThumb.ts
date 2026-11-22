"use client";

import { useCallback, useLayoutEffect, useRef, useState } from "react";

export type ThumbRect = { x: number; y: number; width: number; height: number };

/**
 * 在容器内测量当前激活项的位置，供单个绝对定位的高亮滑块/胶囊 thumb 平移。
 * 依赖 activeKey 触发重测；容器与窗口尺寸、字体加载都会重新测量。
 */
export function useSlidingThumb<T extends HTMLElement>(activeKey: string) {
  const containerRef = useRef<T | null>(null);
  const [rect, setRect] = useState<ThumbRect | null>(null);

  const measure = useCallback(() => {
    const root = containerRef.current;
    if (!root) {
      setRect(null);
      return;
    }
    const active = root.querySelector<HTMLElement>('[data-active="true"]');
    // 隐藏的导航（例如窄屏时的侧栏）不参与测量。
    if (!active || active.offsetParent === null) {
      setRect(null);
      return;
    }
    setRect({
      x: active.offsetLeft,
      y: active.offsetTop,
      width: active.offsetWidth,
      height: active.offsetHeight,
    });
  }, []);

  useLayoutEffect(() => {
    measure();
    const root = containerRef.current;
    if (!root) return;
    const observer = new ResizeObserver(measure);
    observer.observe(root);
    window.addEventListener("resize", measure);
    void document.fonts?.ready.then(measure).catch(() => undefined);
    return () => {
      observer.disconnect();
      window.removeEventListener("resize", measure);
    };
  }, [activeKey, measure]);

  return { containerRef, rect, measure };
}
