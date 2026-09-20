/// <reference types="vite/client" />

interface ImportMetaEnv {
  /** 后端 API 基础地址；未配置时默认同源（生产由反向代理提供 /api） */
  readonly VITE_API_BASE?: string
}

interface ImportMeta {
  readonly env: ImportMetaEnv
}
