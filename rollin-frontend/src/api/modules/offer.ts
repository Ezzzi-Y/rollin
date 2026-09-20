/**
 * 候选人公开 Offer 接口（契约 §7 Public Offer 三端点）。
 *
 * - 无 Session / 无 Cookie 依赖，凭邮件中的 Offer Token 访问，免 CSRF；
 * - GET 一律零副作用（A13）：页面加载只查询，accept / decline 仅由用户点击触发；
 * - accept / decline 幂等（88.5.4）：重复提交返回既有终态，前端据此平滑切换到结果视图；
 * - 全部显式跳过 401 全局跳转：Offer 页面绝不把候选人带到任何登录入口（需求 §76）。
 * - 响应一律经 zod 校验后再交付页面，异常数据归一化为统一的中文错误。
 */
import { z } from 'zod'

import { ApiError, http } from '../client'

// ---------- 响应校验（契约 §7.1–7.3，逐字段对齐） ----------

/** RFC3339 UTC 时间字符串；允许浏览器 Date 解析以便倒计时/展示 */
const rfc3339 = z
  .string()
  .refine((value) => !Number.isNaN(Date.parse(value)), { message: '非法的 RFC3339 时间' })

const offerStatusSchema = z.enum(['PENDING', 'ACCEPTED', 'DECLINED', 'EXPIRED'])

/**
 * effectiveStatus 计算态（契约 §7.1）：
 * EXPIRED 含「PENDING 但已过截止时间」的未结算计算态；
 * INACTIVE = 活动 DISABLED / ARCHIVED（DISABLED 走 403 错误体，归档为 200 展示态）。
 */
const effectiveStatusSchema = z.enum(['PENDING', 'EXPIRED', 'ACCEPTED', 'DECLINED', 'INACTIVE'])

/** GET /api/public/offers/{token} 响应（契约 §7.1） */
export const publicOfferSchema = z.object({
  activity: z.object({ title: z.string() }),
  candidateName: z.string(),
  message: z.string(),
  status: offerStatusSchema,
  effectiveStatus: effectiveStatusSchema,
  actionable: z.boolean(),
  expiresAt: rfc3339.nullish(),
  serverTime: rfc3339.nullish(),
  /** 仅 ACCEPTED 附带（活动自定义的成功提示文案） */
  successMessage: z.string().nullish(),
})

/** POST accept / decline 响应（契约 §7.2 / §7.3；幂等重复提交返回既有终态） */
export const offerActionSchema = z.object({
  status: offerStatusSchema,
  effectiveStatus: effectiveStatusSchema,
  actionable: z.boolean(),
  successMessage: z.string().nullish(),
  acceptedAt: rfc3339.nullish(),
  declinedAt: rfc3339.nullish(),
})

export type PublicOffer = z.infer<typeof publicOfferSchema>
export type OfferActionResult = z.infer<typeof offerActionSchema>
export type OfferEffectiveStatus = PublicOffer['effectiveStatus']

/** 响应体不符合契约时的归一化错误（message 面向用户） */
function parseResponse<T>(schema: z.ZodType<T>, data: unknown): T {
  const result = schema.safeParse(data)
  if (!result.success) {
    throw new ApiError({ code: 'RESPONSE_INVALID', message: '服务器返回的数据格式异常，请稍后重试' })
  }
  return result.data
}

// ---------- 三端点 ----------

/** 查看 Offer（GET /api/public/offers/{token}）；零副作用，可安全重试 */
export async function getPublicOffer(token: string): Promise<PublicOffer> {
  const data = await http.get<unknown>(`/api/public/offers/${encodeURIComponent(token)}`, {
    skipUnauthorizedRedirect: true,
  })
  return parseResponse(publicOfferSchema, data)
}

/** 接受 Offer（POST /api/public/offers/{token}/accept）；需页面二次确认后调用 */
export async function acceptOffer(token: string): Promise<OfferActionResult> {
  const data = await http.post<unknown>(
    `/api/public/offers/${encodeURIComponent(token)}/accept`,
    undefined,
    { skipUnauthorizedRedirect: true },
  )
  return parseResponse(offerActionSchema, data)
}

/** 放弃 Offer（POST /api/public/offers/{token}/decline）；需强二次确认后调用 */
export async function declineOffer(token: string): Promise<OfferActionResult> {
  const data = await http.post<unknown>(
    `/api/public/offers/${encodeURIComponent(token)}/decline`,
    undefined,
    { skipUnauthorizedRedirect: true },
  )
  return parseResponse(offerActionSchema, data)
}
