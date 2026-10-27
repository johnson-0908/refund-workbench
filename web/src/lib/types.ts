export type RefundStatus = 'PROCESSING' | 'UNKNOWN' | 'SUCCEEDED' | 'FAILED'
export interface Refund {
  refund_id: string
  order_id: string
  amount_cents: number
  provider_request_id: string
  reason: string
  status: RefundStatus
  message: string
  created_at: string
  updated_at: string
}
export interface Order {
  data_version: number
  order_id: string
  merchant_id: string
  amount_cents: number
  status: 'PAID' | 'UNPAID'
  refund: Refund | null
}
export interface Activity {
  event_id: string
  merchant_id: string
  refund_id: string
  order_id: string
  amount_cents: number
  status: RefundStatus
  message: string
  occurred_at: string
}
export type IdentityId = 'a-write' | 'a-read' | 'b-write'
export type Permission = 'orders:read' | 'refunds:read' | 'refunds:create' | 'refunds:reconcile' | 'data:reset'
export interface SessionUser {
  identity_id: IdentityId
  merchant_id: string
  merchant_name: string
  display_name: string
  role: 'operator' | 'viewer'
  permissions: Permission[]
}
export interface AuthSession { user: SessionUser; csrf_token: string; expires_at: string }
export interface ResetResult { reset_id: string; merchant_id: string; order_count: number; data_version: number; reset_at: string; message: string }

// Login choices are presentation only; business authorization comes from the server session.
export const loginIdentities = [
  { id: 'a-write', merchant_id: 'A', merchant_name: '华东商贸', label: '财务经办', description: '订单查询、退款申请与处理' },
  { id: 'a-read', merchant_id: 'A', merchant_name: '华东商贸', label: '财务查看', description: '订单与退款记录查询' },
  { id: 'b-write', merchant_id: 'B', merchant_name: '华南商贸', label: '财务经办', description: '订单查询、退款申请与处理' },
] as const
