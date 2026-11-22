"use client";

import Link from "next/link";
import { usePathname } from "next/navigation";
import { useCallback, useEffect, useMemo, useRef, useState, type ReactNode } from "react";

import { BrandMark } from "./BrandMark";
import { Icon } from "./Icon";
import {
  SETTINGS_ITEM,
  isActive,
  pageMeta,
  roleLabel,
  visibleGroups,
  type NavItem,
} from "./nav";
import { SIDEBAR_DEFAULT, SIDEBAR_KEY, SIDEBAR_MAX, SIDEBAR_MIN, clampSidebarWidth, parseStoredWidth } from "./sidebar-width";
import { useSlidingThumb } from "./useSlidingThumb";

type Session = { account: string; displayName: string; role: string };

function navClass(active: boolean, disabled?: boolean): string {
  return ["shell-nav-item", active ? "on" : "", disabled ? "disabled" : ""].filter(Boolean).join(" ");
}

export default function AppShellClient({
  session,
  gatewayUrl,
  version,
  license,
  children,
}: {
  session: Session;
  gatewayUrl: string;
  version: string;
  license?: { status: string; message: string; writable: boolean };
  children: ReactNode;
}) {
  const pathname = usePathname() ?? "/";
  const groups = useMemo(() => visibleGroups(session.role), [session.role]);
  const meta = useMemo(() => pageMeta(pathname), [pathname]);
  const settingsActive = isActive(pathname, SETTINGS_ITEM);
  const [width, setWidth] = useState(SIDEBAR_DEFAULT);
  const dragRef = useRef<{ startX: number; startWidth: number } | null>(null);
  const topNavRef = useRef<HTMLDivElement | null>(null);

  const sidebarThumb = useSlidingThumb<HTMLDivElement>(`${pathname}|${session.role}`);
  const topThumb = useSlidingThumb<HTMLDivElement>(`${pathname}|${session.role}`);

  useEffect(() => {
    const stored = parseStoredWidth(window.localStorage.getItem(SIDEBAR_KEY));
    setWidth(stored);
    document.documentElement.style.setProperty("--sidebar-w", `${stored}px`);
  }, []);

  const applyWidth = useCallback((next: number, persist: boolean) => {
    const clamped = clampSidebarWidth(next);
    setWidth(clamped);
    document.documentElement.style.setProperty("--sidebar-w", `${clamped}px`);
    if (persist) window.localStorage.setItem(SIDEBAR_KEY, String(clamped));
  }, []);

  useEffect(() => {
    const node = topNavRef.current?.querySelector<HTMLElement>('[data-active="true"]');
    node?.scrollIntoView({ block: "nearest", inline: "center", behavior: "smooth" });
  }, [pathname]);

  const gatewayLabel = (() => {
    try {
      const url = new URL(gatewayUrl);
      return `${url.hostname}:${url.port || "80"}`;
    } catch {
      return gatewayUrl || "未配置";
    }
  })();

  function renderItem(item: NavItem, onNavigate?: () => void) {
    const active = isActive(pathname, item);
    const content = (
      <>
        <Icon name={item.icon} size={16} />
        <span>{item.label}</span>
      </>
    );
    if (item.disabled || !item.href) {
      return (
        <span
          className={navClass(false, true)}
          aria-disabled="true"
          title={item.tooltip ?? "尚未实现"}
          key={item.id}
        >
          {content}
        </span>
      );
    }
    return (
      <Link
        key={item.id}
        href={item.href}
        className={navClass(active)}
        aria-current={active ? "page" : undefined}
        data-active={active}
        onClick={onNavigate}
      >
        {content}
      </Link>
    );
  }

  const topItems: NavItem[] = [
    ...groups.flatMap((group) => group.items),
    SETTINGS_ITEM,
  ];

  return (
    <div className="shell">
      <a className="skip-link" href="#main">跳到正文</a>

      <aside className="shell-sidebar" aria-label="主导航">
        <div className="shell-brand">
          <span className="shell-brand-mark"><BrandMark size={20} /></span>
          <span className="shell-brand-text">
            <strong>KeyKeep</strong>
            <small>AI Gateway</small>
          </span>
        </div>

        <div className="shell-nav-scroll" ref={sidebarThumb.containerRef}>
          {sidebarThumb.rect ? (
            <span
              className="shell-nav-highlight"
              aria-hidden="true"
              style={{
                transform: `translate3d(0, ${sidebarThumb.rect.y}px, 0)`,
                height: sidebarThumb.rect.height,
              }}
            />
          ) : null}
          {groups.map((group) => (
            <div className="shell-nav-group" key={group.id}>
              {group.label ? <div className="shell-nav-label">{group.label}</div> : null}
              {group.items.map((item) => renderItem(item))}
            </div>
          ))}
        </div>

        <div className="shell-foot">
          <span className="shell-gw" title={gatewayUrl ? `网关：${gatewayUrl}` : "网关地址未配置"}>
            <i className={gatewayUrl ? "on" : ""} aria-hidden="true" />
            <span>网关 {gatewayLabel}</span>
          </span>
          <Link
            href={SETTINGS_ITEM.href as string}
            className={navClass(settingsActive)}
            aria-current={settingsActive ? "page" : undefined}
            data-active={settingsActive}
          >
            <Icon name={SETTINGS_ITEM.icon} size={16} />
            <span>{SETTINGS_ITEM.label}</span>
          </Link>
          <div className="shell-user" title={`${session.displayName} · ${roleLabel(session.role)}`}>
            <span className="avatar">{session.displayName.slice(0, 1)}</span>
            <span className="shell-user-text">
              <strong>{session.displayName}</strong>
              <small>{roleLabel(session.role)}</small>
            </span>
          </div>
          <div className="shell-version">v{version}</div>
        </div>

        <div
          className="shell-resize"
          role="separator"
          aria-orientation="vertical"
          aria-label="调整侧栏宽度"
          aria-valuemin={SIDEBAR_MIN}
          aria-valuemax={SIDEBAR_MAX}
          aria-valuenow={width}
          tabIndex={0}
          onPointerDown={(event) => {
            event.currentTarget.setPointerCapture(event.pointerId);
            dragRef.current = { startX: event.clientX, startWidth: width };
          }}
          onPointerMove={(event) => {
            if (!dragRef.current) return;
            applyWidth(dragRef.current.startWidth + event.clientX - dragRef.current.startX, false);
          }}
          onPointerUp={(event) => {
            if (!dragRef.current) return;
            event.currentTarget.releasePointerCapture(event.pointerId);
            dragRef.current = null;
            applyWidth(width, true);
          }}
          onDoubleClick={() => applyWidth(SIDEBAR_DEFAULT, true)}
          onKeyDown={(event) => {
            if (event.key === "ArrowLeft") {
              event.preventDefault();
              applyWidth(width - 8, true);
            } else if (event.key === "ArrowRight") {
              event.preventDefault();
              applyWidth(width + 8, true);
            } else if (event.key === "Home") {
              event.preventDefault();
              applyWidth(SIDEBAR_MIN, true);
            } else if (event.key === "End") {
              event.preventDefault();
              applyWidth(SIDEBAR_MAX, true);
            }
          }}
        />
      </aside>

      <div className="shell-main">
        <header className="shell-topbar">
          <div className="shell-topbrand">
            <BrandMark size={18} />
            <strong>KeyKeep</strong>
          </div>
          <nav className="shell-topnav" aria-label="主导航" ref={topThumb.containerRef}>
            <div className="shell-topnav-track" ref={topNavRef}>
              {topThumb.rect ? (
                <span
                  className="shell-topnav-thumb"
                  aria-hidden="true"
                  style={{
                    transform: `translate3d(${topThumb.rect.x}px, 0, 0)`,
                    width: topThumb.rect.width,
                    height: topThumb.rect.height,
                  }}
                />
              ) : null}
              {topItems.map((item, index) => (
                <span className="shell-topnav-slot" key={item.id}>
                  {index === topItems.length - 1 ? <span className="shell-topnav-sep" aria-hidden="true" /> : null}
                  {renderItem(item)}
                </span>
              ))}
            </div>
          </nav>
          <div className="shell-crumbs">
            {meta.group ? <><span>{meta.group}</span><span>/</span></> : null}
            <strong>{meta.label}</strong>
          </div>
        </header>

        {license && !license.writable ? (
          <div className="license-banner" role="status">
            <span>只读：{license.message}</span>
            <Link href="/license">授权详情</Link>
          </div>
        ) : null}

        <main id="main" className="shell-content" tabIndex={-1}>
          <div className="page-enter" key={pathname}>{children}</div>
        </main>
      </div>
    </div>
  );
}
