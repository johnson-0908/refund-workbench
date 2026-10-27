import type { RefundStatus } from './types'

export function money(cents: number): string {
  if (!Number.isSafeInteger(cents)) return '金额异常'
  const sign = cents < 0 ? '-' : ''
  const abs = Math.abs(cents)
  return `${sign}¥${Math.floor(abs / 100).toLocaleString('zh-CN')}.${String(abs % 100).padStart(2, '0')}`
}

export function dateTime(value?: string): string {
  if (!value) return '—'
  const date = new Date(value)
  if (Number.isNaN(date.getTime())) return '—'
  return new Intl.DateTimeFormat('zh-CN', {
    timeZone: 'Asia/Shanghai', year: 'numeric', month: '2-digit', day: '2-digit',
    hour: '2-digit', minute: '2-digit', second: '2-digit', hour12: false,
  }).format(date)
}

export const statusInfo: Record<RefundStatus, { label: string; color: string; hint: string }> = {
  PROCESSING: { label: '处理中', color: 'processing', hint: '渠道正在处理。只能查询原退款结果，请勿重复发起。' },
  UNKNOWN: { label: '待确认', color: 'warning', hint: '尚未收到渠道明确结果，不代表退款失败。请查询原退款结果。' },
  SUCCEEDED: { label: '退款成功', color: 'success', hint: '渠道已确认退款成功，本订单不能再次申请退款。' },
  FAILED: { label: '退款失败', color: 'error', hint: '支付渠道已拒绝退款，本订单不支持重新申请。' },
}
