import type { Metadata } from "next";
import "./globals.css";

export const metadata: Metadata = {
  title: "KeyKeep · 控制台",
  description: "私有化部署的企业 AI API 网关与虚拟密钥控制台。",
};

export default function RootLayout({ children }: Readonly<{ children: React.ReactNode }>) {
  return (
    <html lang="zh-CN" data-theme="dark" suppressHydrationWarning>
      <head>
        <script
          dangerouslySetInnerHTML={{
            __html:
              // 内联脚本在首帧前跑：默认深色，只有用户显式选过浅色才切回去（避免白屏闪烁）。
              "try{document.documentElement.dataset.theme=localStorage.getItem('kk-theme')==='light'?'light':'dark';var w=parseInt(localStorage.getItem('kk-sidebar-w')||'212',10);if(w>=168&&w<=320)document.documentElement.style.setProperty('--sidebar-w',w+'px');if(localStorage.getItem('kk-motion')==='reduced')document.documentElement.dataset.motion='reduced'}catch(e){}",
          }}
        />
      </head>
      <body>{children}</body>
    </html>
  );
}
