"use client";

import { useCallback, useEffect, useState } from "react";

export type Theme = "light" | "dark";

const KEY = "kk-theme";

/** 默认深色：只有用户显式选过「浅色」才回到亮色，首装/清缓存一律深色。 */
const DEFAULT_THEME: Theme = "dark";

function read(): Theme {
  if (typeof window === "undefined") return DEFAULT_THEME;
  const saved = window.localStorage.getItem(KEY);
  return saved === "light" ? "light" : DEFAULT_THEME;
}

/** 主题状态：localStorage 持久化，跨组件用自定义事件同步。 */
export function useTheme() {
  const [theme, setThemeState] = useState<Theme>(DEFAULT_THEME);

  useEffect(() => {
    setThemeState(read());
    const sync = () => setThemeState(read());
    window.addEventListener("storage", sync);
    window.addEventListener("kk-theme-change", sync);
    return () => {
      window.removeEventListener("storage", sync);
      window.removeEventListener("kk-theme-change", sync);
    };
  }, []);

  const setTheme = useCallback((next: Theme) => {
    document.documentElement.dataset.theme = next;
    window.localStorage.setItem(KEY, next);
    window.dispatchEvent(new Event("kk-theme-change"));
    setThemeState(next);
  }, []);

  return { theme, setTheme };
}
