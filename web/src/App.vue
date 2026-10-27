<script setup lang="ts">
import { computed, onMounted, onUnmounted, ref, watch } from 'vue'
import zhCN from 'ant-design-vue/es/locale/zh_CN'
import { AppstoreOutlined, ArrowRightOutlined, AuditOutlined, CheckCircleOutlined, ClockCircleOutlined,
  BankOutlined, ExclamationCircleOutlined, FileSearchOutlined, LockOutlined, LogoutOutlined,
  ReloadOutlined, RollbackOutlined, SafetyCertificateOutlined, SearchOutlined, SwapOutlined, WalletOutlined } from '@ant-design/icons-vue'
import StatusBadge from './components/StatusBadge.vue'
import LoginPage from './components/LoginPage.vue'
import { useWorkbench } from './composables/useWorkbench'
import { dateTime, money, statusInfo } from './lib/format'
import type { Order } from './lib/types'

const wb = useWorkbench()
const { user, session, authLoading, authBusy, authError, logoutPending, orders, selectedId, detail, activities, activityAvailable, listLoading, detailLoading,
  activityLoading, actionLoading, resetLoading, resetError, resetNotice, canReset, dataVersion, listError, detailError, actionError, notice, pendingIntent, canRead, canCreate, canReconcile } = wb
const search = ref('')
const filter = ref('all')
const reason = ref('')
const confirmOpen = ref(false)
const resetConfirmOpen = ref(false)
const resetConfirmation = ref('')
const filters = [{ value: 'all', label: '全部订单' }, { value: 'eligible', label: '可申请' }, { value: 'pending', label: '待处理' }, { value: 'terminal', label: '已结束' }]
const pending = computed(() => orders.value.filter((o) => o.refund && ['UNKNOWN', 'PROCESSING'].includes(o.refund.status)))
const succeeded = computed(() => orders.value.filter((o) => o.refund?.status === 'SUCCEEDED'))
const eligible = computed(() => orders.value.filter((o) => o.status === 'PAID' && !o.refund))
const filteredOrders = computed(() => orders.value.filter((order) => {
  if (search.value && !order.order_id.toLowerCase().includes(search.value.toLowerCase().trim())) return false
  if (filter.value === 'eligible') return order.status === 'PAID' && !order.refund
  if (filter.value === 'pending') return order.refund && ['UNKNOWN', 'PROCESSING'].includes(order.refund.status)
  if (filter.value === 'terminal') return order.refund && ['SUCCEEDED', 'FAILED'].includes(order.refund.status)
  return true
}))
const columns = [
  { title: '订单信息', key: 'order', width: 145 },
  { title: '订单金额', key: 'amount', width: 114 },
  { title: '退款状态', key: 'refund', width: 116 },
  { title: '最近更新', key: 'updated', width: 136 },
  { title: '', key: 'action', width: 48 },
]
const reasonValue = computed(() => pendingIntent.value?.reason ?? reason.value)
const reasonCount = computed(() => Array.from(reasonValue.value).length)
const validReason = computed(() => !!reasonValue.value.trim() && reasonCount.value <= 200)
const currentInfo = computed(() => detail.value?.refund ? statusInfo[detail.value.refund.status] : null)
const unresolved = computed(() => detail.value?.refund && ['UNKNOWN', 'PROCESSING'].includes(detail.value.refund.status))

watch(() => session.value?.csrf_token, () => { search.value = ''; filter.value = 'all'; reason.value = ''; confirmOpen.value = false; resetConfirmOpen.value = false; resetConfirmation.value = '' }, { flush: 'sync' })
watch(dataVersion, () => { reason.value = ''; confirmOpen.value = false; resetConfirmOpen.value = false; resetConfirmation.value = '' }, { flush: 'sync' })
watch(selectedId, () => { reason.value = ''; confirmOpen.value = false }, { flush: 'sync' })
watch(() => detail.value?.refund, (refund) => { if (refund) confirmOpen.value = false })

function rowProps(record: Order) {
  return { onClick: () => wb.selectOrder(record.order_id), class: selectedId.value === record.order_id ? 'selected-row' : '' }
}
async function submitConfirmed() {
  if (!validReason.value || resetLoading.value) return
  await wb.submitRefund(reasonValue.value)
  confirmOpen.value = false
}
function openResetConfirmation() {
  if (!canReset.value || resetLoading.value || actionLoading.value || authBusy.value) return
  confirmOpen.value = false
  resetConfirmation.value = ''
  resetConfirmOpen.value = true
}
async function confirmReset() {
  if (resetConfirmation.value !== '恢复初始数据' || !canReset.value || resetLoading.value || actionLoading.value) return
  resetConfirmOpen.value = false
  confirmOpen.value = false
  reason.value = ''
  resetConfirmation.value = ''
  await wb.resetData('恢复初始数据')
}
onMounted(() => { void wb.restoreSession() })
onUnmounted(wb.dispose)
</script>

<template>
  <a-config-provider :locale="zhCN" :theme="{ token: { colorPrimary: '#16766a', borderRadius: 8, fontFamily: 'Inter, PingFang SC, Microsoft YaHei, sans-serif', controlHeight: 36 } }">
    <LoginPage v-if="!user || authLoading" :loading="authLoading" :busy="authBusy" :error="authError" :logout-pending="logoutPending" @login="wb.login" @retry-logout="wb.logout" />
    <div v-else class="app-shell" data-testid="erp-shell">
      <aside class="sidebar">
        <a href="#" class="brand" aria-label="易数链动企业管理"><span class="brand-mark"><SwapOutlined /></span><span>易数链动<small>企业管理平台</small></span></a>
        <div class="sidebar-label">财务管理</div>
        <div class="nav-item active"><WalletOutlined /><span>订单退款</span><span class="nav-indicator" /></div>
        <div class="sidebar-note"><BankOutlined /><div>{{ user.merchant_name }}<p>企业财务工作台</p></div></div>
      </aside>

      <div class="main-shell">
        <header class="topbar">
          <div class="breadcrumb">财务管理<span>/</span><strong>订单退款</strong></div>
          <div class="identity-control"><span class="company-name"><BankOutlined />{{ user.merchant_name }}</span><span class="topbar-divider" />
            <span class="avatar">{{ user.display_name.slice(-2) }}</span><span class="user-role">{{ user.display_name }}<small>{{ user.role === 'operator' ? '经办权限' : '查看权限' }}</small></span>
            <a-button type="text" size="small" class="switch-company" :disabled="authBusy" @click="wb.logout">切换企业</a-button>
            <a-button type="text" size="small" :disabled="authBusy" data-testid="logout-button" @click="wb.logout"><LogoutOutlined /><span class="logout-label">退出</span></a-button>
          </div>
        </header>

        <main class="workspace">
          <section class="page-heading">
            <div><h1>订单退款</h1><p>查询订单，处理退款申请与退款结果。</p></div>
            <div class="heading-actions"><a-button v-if="canReset" type="text" class="reset-data-trigger" :disabled="resetLoading || actionLoading || authBusy" data-testid="reset-data-button" @click="openResetConfirmation"><template #icon><RollbackOutlined /></template>恢复初始数据</a-button><a-button :loading="listLoading" :disabled="resetLoading" @click="wb.refreshAll"><template #icon><ReloadOutlined /></template>刷新数据</a-button></div>
          </section>

          <a-alert v-if="resetLoading" type="info" show-icon class="reset-feedback" message="正在恢复初始数据，请稍候…" />
          <a-alert v-if="resetNotice" type="success" show-icon class="reset-feedback" :message="resetNotice" :description="resetNotice.startsWith('已恢复 ') ? '原业务记录已自动归档，其他企业数据保持不变。' : undefined" data-testid="reset-success" />
          <a-alert v-if="resetError" type="warning" show-icon class="reset-feedback" :message="resetError" data-testid="reset-error"><template #action><a-button size="small" :disabled="resetLoading" @click="wb.refreshAll">刷新订单</a-button></template></a-alert>

          <a-alert v-if="!canRead" type="warning" show-icon class="permission-banner" message="当前账号暂无订单与退款查看权限，请联系管理员。" />
          <a-alert v-else-if="!canCreate && !canReconcile" type="info" show-icon class="permission-banner" message="当前为查看权限，可查询订单及退款记录。" />

          <section class="stats-grid" aria-label="当前商户订单汇总">
            <div class="stat-card"><span class="stat-icon green"><AppstoreOutlined /></span><div><p>订单总数</p><strong>{{ listError ? '—' : orders.length }}<small>笔</small></strong></div><span class="stat-caption">全部订单</span></div>
            <div class="stat-card"><span class="stat-icon blue"><FileSearchOutlined /></span><div><p>可申请退款</p><strong>{{ listError ? '—' : eligible.length }}<small>笔</small></strong></div><span class="stat-caption">已支付 · 无退款</span></div>
            <div class="stat-card"><span class="stat-icon amber"><ClockCircleOutlined /></span><div><p>待处理退款</p><strong>{{ listError ? '—' : pending.length }}<small>笔</small></strong></div><span class="stat-caption">处理中 · 待确认</span></div>
            <div class="stat-card"><span class="stat-icon green"><CheckCircleOutlined /></span><div><p>退款成功</p><strong>{{ listError ? '—' : succeeded.length }}<small>笔</small></strong></div><span class="stat-caption">已完成</span></div>
          </section>

          <div class="workspace-grid">
            <div class="orders-column">
              <section class="panel orders-panel">
                <div class="panel-header"><div><h2>订单列表<span class="count-chip">{{ orders.length }}</span></h2></div></div>
                <div class="table-toolbar"><a-segmented v-model:value="filter" :options="filters" aria-label="筛选订单" /><a-input v-model:value="search" placeholder="搜索订单编号" allow-clear class="order-search" aria-label="搜索订单编号"><template #prefix><SearchOutlined /></template></a-input></div>
                <a-alert v-if="listError" data-testid="list-error" type="error" show-icon :message="listError" class="table-alert"><template #action><a-button size="small" @click="wb.refreshOrders">重试</a-button></template></a-alert>
                <a-table :columns="columns" :data-source="filteredOrders" :loading="listLoading || resetLoading" :pagination="false" :custom-row="rowProps" row-key="order_id" :scroll="{ x: 560 }" class="orders-table">
                  <template #bodyCell="{ column, record }">
                    <template v-if="column.key === 'order'"><button class="order-id" @click.stop="wb.selectOrder(record.order_id)">{{ record.order_id }}</button><div class="payment-label"><span :class="['payment-dot', record.status === 'PAID' ? 'paid' : 'unpaid']" />{{ record.status === 'PAID' ? '已支付' : '未支付' }}</div></template>
                    <template v-else-if="column.key === 'amount'"><span class="table-amount">{{ money(record.amount_cents) }}</span></template>
                    <template v-else-if="column.key === 'refund'"><StatusBadge :status="record.refund?.status ?? null" /></template>
                    <template v-else-if="column.key === 'updated'"><span class="table-date">{{ record.refund ? dateTime(record.refund.updated_at) : '—' }}</span></template>
                    <template v-else-if="column.key === 'action'"><button class="table-view" :aria-label="`查看订单 ${record.order_id}`" @click.stop="wb.selectOrder(record.order_id)"><ArrowRightOutlined /></button></template>
                  </template>
                  <template #emptyText><a-empty data-testid="orders-empty"><template #description><span>{{ listError ? '暂时无法读取订单，请重试' : search || filter !== 'all' ? '暂无符合条件的订单' : '当前商户暂无订单' }}</span></template></a-empty></template>
                </a-table>
                <div class="table-foot"><span>{{ user.merchant_name }}</span><span>共 {{ filteredOrders.length }} 笔</span></div>
              </section>

              <section class="panel activity-panel">
                <div class="panel-header"><div><h2><AuditOutlined />最近动态</h2></div><a-button type="text" :loading="activityLoading" aria-label="刷新处理动态" @click="wb.refreshActivity"><ReloadOutlined /></a-button></div>
                <div v-if="!activityAvailable" class="activity-unavailable"><ExclamationCircleOutlined /><span>处理动态暂不可用，不影响订单查询与退款处理。</span></div>
                <div v-else-if="activities.length === 0" class="activity-empty"><span class="empty-line" />暂无处理记录</div>
                <div v-else class="activity-list"><div v-for="event in activities.slice(0, 5)" :key="event.event_id" class="activity-item"><span :class="['activity-dot', event.status.toLowerCase()]" /><div><strong>{{ event.order_id }}</strong><span>{{ statusInfo[event.status].label }}</span><p>{{ event.message }}</p></div><time>{{ dateTime(event.occurred_at) }}</time></div></div>
              </section>
            </div>

            <section class="panel detail-panel" aria-label="订单详情">
              <div class="panel-header"><div><h2>订单详情</h2></div><a-button v-if="selectedId" type="text" aria-label="刷新订单详情" :loading="detailLoading" @click="wb.selectOrder(selectedId)"><ReloadOutlined /></a-button></div>
              <div v-if="detailLoading" class="detail-placeholder"><a-spin /><p>正在读取订单…</p></div>
              <div v-else-if="detailError" class="detail-error"><a-alert type="error" :message="detailError" show-icon /><a-button @click="wb.selectOrder(selectedId)">重试查询</a-button></div>
              <div v-else-if="!detail" class="detail-placeholder"><span class="detail-empty-icon"><FileSearchOutlined /></span><h3>选择一笔订单</h3><p>查看订单信息与退款记录</p></div>
              <div v-else class="detail-content">
                <div class="detail-order-top"><span class="detail-order-id">{{ detail.order_id }}</span><a-tag :color="detail.status === 'PAID' ? 'green' : 'default'">{{ detail.status === 'PAID' ? '已支付' : '未支付' }}</a-tag></div>
                <div class="detail-amount"><span>订单金额 / 全额退款</span><strong>{{ money(detail.amount_cents) }}</strong></div>
                <dl class="metadata"><div><dt>所属企业</dt><dd>{{ user.merchant_name }}</dd></div><div><dt>退款状态</dt><dd><StatusBadge :status="detail.refund?.status ?? null" /></dd></div></dl>

                <template v-if="detail.refund && currentInfo">
                  <div :class="['result-callout', detail.refund.status.toLowerCase()]"><strong>{{ currentInfo.label }}</strong><p>{{ currentInfo.hint }}</p></div>
                  <dl class="metadata refund-metadata"><div><dt>退款单号</dt><dd class="mono">{{ detail.refund.refund_id }}</dd></div><div><dt>退款金额</dt><dd>{{ money(detail.refund.amount_cents) }}</dd></div><div><dt>渠道请求号</dt><dd class="mono">{{ detail.refund.provider_request_id }}</dd></div><div><dt>退款原因</dt><dd>{{ detail.refund.reason }}</dd></div><div><dt>渠道说明</dt><dd>{{ detail.refund.message || '暂无额外说明' }}</dd></div><div><dt>申请时间</dt><dd>{{ dateTime(detail.refund.created_at) }}</dd></div><div><dt>更新时间</dt><dd>{{ dateTime(detail.refund.updated_at) }}</dd></div></dl>
                  <a-button v-if="unresolved && canReconcile" type="primary" block :loading="actionLoading" :disabled="resetLoading" data-testid="reconcile-button" @click="wb.reconcile"><template #icon><SearchOutlined /></template>查询处理结果</a-button>
                  <p v-if="unresolved" class="action-helper">{{ canReconcile ? '查询原退款进度，不会重复申请。' : '如需查证退款进度，请联系财务经办人员。' }}</p>
                  <div v-else class="terminal-note"><LockOutlined />本订单退款流程已结束，不支持重新申请</div>
                </template>
                <template v-else-if="detail.status !== 'PAID'"><div class="result-callout neutral"><strong>当前订单尚未支付</strong><p>未支付订单不满足退款条件，无法申请退款。</p></div><a-button block disabled><LockOutlined />未支付，不可退款</a-button></template>
                <template v-else-if="canCreate">
                  <div class="form-divider" />
                  <a-alert v-if="pendingIntent" type="warning" show-icon message="原申请等待确认" description="请先刷新查询；如需重试，将继续处理原申请。" class="pending-alert" />
                  <label for="refund-reason" class="field-label">退款原因<span>*</span></label>
                  <a-textarea id="refund-reason" :value="reasonValue" :disabled="!!pendingIntent || actionLoading || resetLoading" :rows="3" placeholder="请填写退款原因" @update:value="(value: string) => reason = value" />
                  <div class="field-count" :class="{ invalid: reasonCount > 200 }">{{ reasonCount }} / 200</div>
                  <a-button type="primary" block :disabled="!validReason || actionLoading || resetLoading" data-testid="create-refund-button" @click="confirmOpen = true"><template #icon><WalletOutlined /></template>{{ pendingIntent ? '重试原申请' : '申请全额退款' }}</a-button>
                  <p class="action-helper">每笔订单仅支持一次全额退款。</p>
                </template>
                <div v-else class="terminal-note"><LockOutlined />当前账号仅可查看退款信息</div>
                <a-alert v-if="actionError" type="warning" show-icon :message="actionError" class="action-feedback" />
                <a-alert v-if="notice" type="info" show-icon :message="notice" class="action-feedback" />
              </div>
              <div v-if="detail?.refund" class="detail-footer"><SafetyCertificateOutlined /><span>退款结果以支付渠道确认为准</span></div>
            </section>
          </div>
          <footer class="page-footer"><span>易数链动企业管理平台</span><span>北京时间 · UTC+8</span></footer>
        </main>
      </div>
    </div>

    <a-modal v-model:open="confirmOpen" title="确认全额退款" ok-text="确认并提交" cancel-text="返回检查" :confirm-loading="actionLoading" :mask-closable="!actionLoading" :closable="!actionLoading" :cancel-button-props="{ disabled: actionLoading }" @ok="submitConfirmed">
      <div v-if="detail" class="confirmation-content"><p>请核对订单与退款信息。</p><dl class="metadata"><div><dt>订单编号</dt><dd>{{ detail.order_id }}</dd></div><div><dt>退款金额</dt><dd class="confirmation-amount">{{ money(detail.amount_cents) }}</dd></div><div><dt>退款原因</dt><dd>{{ reasonValue }}</dd></div></dl><a-alert type="warning" show-icon :message="pendingIntent ? '本次将继续处理原申请，不会创建新的退款。' : '退款提交后不可撤销，每笔订单仅支持一次全额退款。'" /></div>
    </a-modal>
    <a-modal v-model:open="resetConfirmOpen" title="恢复初始数据" ok-text="确认恢复" cancel-text="取消" :ok-button-props="{ danger: true, disabled: resetConfirmation !== '恢复初始数据' || resetLoading || actionLoading || !canReset }" @ok="confirmReset">
      <div class="reset-confirmation-content"><p class="reset-company">当前企业：<strong>{{ user?.merchant_name }}</strong></p><a-alert type="warning" show-icon message="此操作将恢复当前企业的初始数据" description="清除退款、渠道及幂等记录，订单恢复初始状态。恢复前将自动归档，其他企业不受影响。" /><label for="reset-confirmation">请输入 <strong>恢复初始数据</strong> 以确认操作</label><a-input id="reset-confirmation" v-model:value="resetConfirmation" autocomplete="off" placeholder="恢复初始数据" :disabled="resetLoading" data-testid="reset-confirmation-input" /></div>
    </a-modal>
  </a-config-provider>
</template>
