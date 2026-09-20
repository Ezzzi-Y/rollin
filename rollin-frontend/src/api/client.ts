/**
 * fetch 封装：
 * - credentials: 'include'（后台认证使用 Session + Cookie）
 * - JSON 序列化 / 反序列化；getBlob 变体供 XLSX 导出等二进制接口使用
 * - 所有失败统一归一化为 { code, message, details? }（契约 §1.3 错误体）
 * - 401（UNAUTHENTICATED / TOKEN_EXPIRED）触发全局回调，由认证层跳转对应登录页
 */

export interface ApiErrorPayload {
  code: string
  message: string
  details?: unknown
}

/** 请求失败的统一错误类型；UI 层据此展示 message、按 code 分支处理 */
export class ApiError extends Error {
  readonly code: string
  readonly details?: unknown

  constructor(payload: ApiErrorPayload) {
    super(payload.message)
    this.name = 'ApiError'
    this.code = payload.code
    this.details = payload.details
  }
}

export type HttpMethod = 'GET' | 'POST' | 'PUT' | 'PATCH' | 'DELETE'

export interface RequestOptions {
  method?: HttpMethod
  body?: unknown
  query?: Record<string, string | number | boolean | undefined>
  signal?: AbortSignal
  /**
   * 跳过 401 全局跳转（会话探测、公开端点等场景使用），
   * 避免「进入应用时查询当前账户」这类探测请求把用户踢到登录页。
   */
  skipUnauthorizedRedirect?: boolean
}

/**
 * API 基础地址：读 import.meta.env.VITE_API_BASE，默认同源。
 * 生产环境由反向代理将同一路径转发给后端，前端无需区分域名。
 */
const baseUrl = import.meta.env.VITE_API_BASE ?? ''

function buildUrl(path: string, query?: RequestOptions['query']): string {
  const url = `${baseUrl}${path}`
  if (!query) return url
  const search = new URLSearchParams()
  for (const [key, value] of Object.entries(query)) {
    if (value !== undefined) search.set(key, String(value))
  }
  const qs = search.toString()
  return qs ? `${url}?${qs}` : url
}

/** 将非 2xx 响应归一化为 ApiError；响应体不是 JSON 时使用默认错误码 */
async function toApiError(response: Response): Promise<ApiError> {
  let code = `HTTP_${response.status}`
  let message = `请求失败（HTTP ${response.status}）`
  let details: unknown
  try {
    const data: unknown = await response.json()
    if (data && typeof data === 'object' && 'message' in data) {
      const payload = data as { code?: unknown; message?: unknown; details?: unknown }
      if (typeof payload.code === 'string') code = payload.code
      if (typeof payload.message === 'string') message = payload.message
      details = payload.details
    }
  } catch {
    // 响应体不是合法 JSON：保持默认归一化结果
  }
  return new ApiError({ code, message, details })
}

/** 401 全局处理回调：由认证层在应用启动时注册 */
type UnauthorizedHandler = (apiPath: string) => void
let unauthorizedHandler: UnauthorizedHandler | null = null

/** 注册 401 全局跳转回调（幂等，重复注册以最后一次为准） */
export function setUnauthorizedHandler(handler: UnauthorizedHandler | null): void {
  unauthorizedHandler = handler
}

function shouldRedirectOnUnauthorized(code: string, status: number, skip: boolean): boolean {
  if (skip) return false
  return status === 401 || code === 'UNAUTHENTICATED' || code === 'TOKEN_EXPIRED'
}

async function doFetch(path: string, init: RequestInit): Promise<Response> {
  try {
    return await fetch(path, init)
  } catch {
    // 网络层失败（断网、DNS、CORS 硬失败等）：归一化为统一错误码
    throw new ApiError({ code: 'NETWORK_ERROR', message: '网络异常，请检查网络连接后重试' })
  }
}

async function request<T>(path: string, options: RequestOptions = {}): Promise<T> {
  const { method = 'GET', body, query, signal, skipUnauthorizedRedirect = false } = options

  const response = await doFetch(buildUrl(path, query), {
    method,
    // 必须携带 Cookie，供后端 Session 鉴权
    credentials: 'include',
    headers: body === undefined ? undefined : { 'Content-Type': 'application/json' },
    body: body === undefined ? undefined : JSON.stringify(body),
    signal,
  })

  if (!response.ok) {
    const error = await toApiError(response)
    if (shouldRedirectOnUnauthorized(error.code, response.status, skipUnauthorizedRedirect)) {
      unauthorizedHandler?.(path)
    }
    throw error
  }
  if (response.status === 204) {
    return undefined as T
  }
  return (await response.json()) as T
}

/** GET 二进制响应（如 XLSX 导出）；非 2xx 仍归一化为 ApiError */
async function requestBlob(path: string, options: RequestOptions = {}): Promise<Blob> {
  const { query, signal, skipUnauthorizedRedirect = false } = options
  const response = await doFetch(buildUrl(path, query), {
    method: 'GET',
    credentials: 'include',
    signal,
  })
  if (!response.ok) {
    const error = await toApiError(response)
    if (shouldRedirectOnUnauthorized(error.code, response.status, skipUnauthorizedRedirect)) {
      unauthorizedHandler?.(path)
    }
    throw error
  }
  return (await response.blob()) as Blob
}

/** 从 Content-Disposition 中解析导出文件名；无附件名时返回 undefined */
export function filenameFromDisposition(disposition: string | null): string | undefined {
  if (!disposition) return undefined
  const utf8Match = /filename\*=UTF-8''([^;]+)/i.exec(disposition)
  if (utf8Match?.[1]) {
    try {
      return decodeURIComponent(utf8Match[1])
    } catch {
      return utf8Match[1]
    }
  }
  const plainMatch = /filename="?([^";]+)"?/i.exec(disposition)
  return plainMatch?.[1]
}

export const http = {
  get<T>(path: string, options?: Omit<RequestOptions, 'method' | 'body'>): Promise<T> {
    return request<T>(path, { ...options, method: 'GET' })
  },
  /** 下载二进制（XLSX 导出等）；配合 filenameFromDisposition 取下载文件名 */
  getBlob(path: string, options?: Omit<RequestOptions, 'method' | 'body'>): Promise<Blob> {
    return requestBlob(path, options)
  },
  post<T>(path: string, body?: unknown, options?: Omit<RequestOptions, 'method' | 'body'>): Promise<T> {
    return request<T>(path, { ...options, method: 'POST', body })
  },
  put<T>(path: string, body?: unknown, options?: Omit<RequestOptions, 'method' | 'body'>): Promise<T> {
    return request<T>(path, { ...options, method: 'PUT', body })
  },
  patch<T>(path: string, body?: unknown, options?: Omit<RequestOptions, 'method' | 'body'>): Promise<T> {
    return request<T>(path, { ...options, method: 'PATCH', body })
  },
  delete<T>(path: string, options?: Omit<RequestOptions, 'method' | 'body'>): Promise<T> {
    return request<T>(path, { ...options, method: 'DELETE' })
  },
}
