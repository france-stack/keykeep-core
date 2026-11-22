"use client";

import { useRef } from "react";

import { useSlidingThumb } from "./useSlidingThumb";

export type TabItem = { id: string; label: string };

/**
 * 胶囊分段控件：单个 thumb 通过 transform 平移，键盘左右切换。
 * 语义为 tablist；面板由调用方渲染并关联 aria-controls。
 */
export function SegmentedTabs({
  items,
  value,
  onChange,
  label,
  className = "",
}: {
  items: TabItem[];
  value: string;
  onChange: (id: string) => void;
  label: string;
  className?: string;
}) {
  const { containerRef, rect } = useSlidingThumb<HTMLDivElement>(value);
  const buttonsRef = useRef<Array<HTMLButtonElement | null>>([]);

  function move(delta: number) {
    const index = items.findIndex((item) => item.id === value);
    const next = Math.min(items.length - 1, Math.max(0, index + delta));
    if (next !== index) onChange(items[next].id);
    buttonsRef.current[next]?.focus();
  }

  return (
    <div className={`seg-tabs ${className}`} role="tablist" aria-label={label} ref={containerRef}>
      {rect ? (
        <span
          className="seg-thumb"
          aria-hidden="true"
          style={{ transform: `translate3d(${rect.x}px, 0, 0)`, width: rect.width, height: rect.height }}
        />
      ) : null}
      {items.map((item, index) => {
        const active = item.id === value;
        return (
          <button
            key={item.id}
            ref={(node) => {
              buttonsRef.current[index] = node;
            }}
            type="button"
            role="tab"
            id={`tab-${item.id}`}
            aria-selected={active}
            aria-controls={`panel-${item.id}`}
            tabIndex={active ? 0 : -1}
            data-active={active}
            className={`seg-tab${active ? " on" : ""}`}
            onClick={() => onChange(item.id)}
            onKeyDown={(event) => {
              if (event.key === "ArrowRight") {
                event.preventDefault();
                move(1);
              } else if (event.key === "ArrowLeft") {
                event.preventDefault();
                move(-1);
              } else if (event.key === "Home") {
                event.preventDefault();
                onChange(items[0].id);
              } else if (event.key === "End") {
                event.preventDefault();
                onChange(items[items.length - 1].id);
              }
            }}
          >
            {item.label}
          </button>
        );
      })}
    </div>
  );
}
