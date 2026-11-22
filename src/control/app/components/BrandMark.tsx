/**
 * KeyKeep mark：倒置的 G（整体旋转 180°），蓝色主笔画 + 棕色中心点。
 * 纯内联 SVG，不依赖外部图片；小尺寸时省略中心点避免糊成一团。
 */
export function BrandMark({ size = 22 }: { size?: number }) {
  const showDot = size >= 20;
  return (
    <svg
      viewBox="0 0 24 24"
      width={size}
      height={size}
      fill="none"
      aria-hidden="true"
      className="brand-mark-svg"
    >
      <g transform="rotate(180 12 12)">
        <path
          d="M17.5 7.6A7 7 0 1 0 19 12"
          stroke="#4F46E5"
          strokeWidth="2.6"
          strokeLinecap="round"
        />
        <path d="M19 12h-4.6" stroke="#4F46E5" strokeWidth="2.6" strokeLinecap="round" />
      </g>
      {showDot ? <circle cx="12" cy="12" r="1.9" fill="#8B5E3C" /> : null}
    </svg>
  );
}

/** 应用图标/登录页大标：奶油底、蓝 G、棕点。 */
export function BrandTile({ size = 56 }: { size?: number }) {
  return (
    <span className="brand-tile" style={{ width: size, height: size, borderRadius: Math.round(size * 0.26) }}>
      <BrandMark size={Math.round(size * 0.68)} />
    </span>
  );
}
