type SkeletonProps = { className?: string; width?: string | number; height?: string | number };

/** 骨架占位：宽度/高度走内联，动画见 globals.css 的 .skeleton。 */
export function Skeleton({ className = "", width, height }: SkeletonProps) {
  return <span className={`skeleton ${className}`} style={{ width, height }} aria-hidden="true" />;
}

export function SkeletonCards({ count = 4 }: { count?: number }) {
  return (
    <div className="kpis" aria-busy="true" aria-label="加载中">
      {Array.from({ length: count }).map((_, index) => (
        <div className="card kpi skeleton-card" key={index}>
          <Skeleton width="42%" height={12} />
          <Skeleton width="64%" height={28} />
          <Skeleton width="50%" height={10} />
        </div>
      ))}
    </div>
  );
}

export function SkeletonTable({ rows = 6, columns = 6 }: { rows?: number; columns?: number }) {
  return (
    <div className="table-wrap" aria-busy="true" aria-label="加载中">
      <table>
        <thead>
          <tr>
            {Array.from({ length: columns }).map((_, index) => (
              <th key={index}><Skeleton width="60%" height={10} /></th>
            ))}
          </tr>
        </thead>
        <tbody>
          {Array.from({ length: rows }).map((_, row) => (
            <tr key={row}>
              {Array.from({ length: columns }).map((_, column) => (
                <td key={column}><Skeleton width={`${55 + ((row + column) % 4) * 10}%`} height={11} /></td>
              ))}
            </tr>
          ))}
        </tbody>
      </table>
    </div>
  );
}
