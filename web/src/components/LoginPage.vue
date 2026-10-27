<script setup lang="ts">
import { computed, ref, watch } from 'vue'
import { ArrowRightOutlined, BankOutlined, CheckOutlined, SafetyCertificateOutlined, SwapOutlined, UserOutlined } from '@ant-design/icons-vue'
import { loginIdentities, type IdentityId } from '../lib/types'

defineProps<{ loading: boolean; busy: boolean; error: string; logoutPending: boolean }>()
const emit = defineEmits<{ login: [identityId: IdentityId]; retryLogout: [] }>()
const merchantId = ref('A')
const identityId = ref<IdentityId>('a-write')
const roles = computed(() => loginIdentities.filter((item) => item.merchant_id === merchantId.value))
watch(merchantId, () => { identityId.value = roles.value[0]!.id })
</script>

<template>
  <main class="login-page" data-testid="login-page">
    <section class="login-brand-panel">
      <div class="login-brand"><span class="brand-mark"><SwapOutlined /></span><div>易数链动<small>企业管理平台</small></div></div>
      <div class="login-brand-copy"><span class="login-eyebrow">FINANCE WORKSPACE</span><h1>企业财务工作台</h1><p>订单与退款，一处掌握。</p><div class="login-illustration" aria-hidden="true"><div class="ledger-spine" /><div class="ledger-row"><span /><i /><i /></div><div class="ledger-row"><span /><i /><i /></div><div class="ledger-row"><span /><i /><i /></div><div class="ledger-check"><CheckOutlined /></div></div></div>
      <div class="login-brand-footer">易数链动 · 企业业务管理</div>
    </section>

    <section class="login-form-panel">
      <div v-if="loading" class="login-card login-restoring" data-testid="session-restoring"><a-spin size="large" /><h2>正在确认登录状态</h2><p>请稍候</p></div>
      <form v-else class="login-card" @submit.prevent="emit('login', identityId)">
        <span class="login-form-kicker">欢迎使用</span><h2>登录工作台</h2><p class="login-subtitle">选择企业与工作身份，即可免密进入。</p>
        <a-alert v-if="error" type="warning" :message="error" show-icon class="login-alert" data-testid="auth-error" />
        <label class="login-label" for="login-merchant">所属企业</label>
        <a-select id="login-merchant" v-model:value="merchantId" size="large" class="login-company-select" :disabled="busy || logoutPending" aria-label="选择企业"><template #suffixIcon><BankOutlined /></template><a-select-option value="A">华东商贸</a-select-option><a-select-option value="B">华南商贸</a-select-option></a-select>
        <label class="login-label role-label">工作身份</label>
        <div class="login-role-list" role="radiogroup" aria-label="选择工作身份"><button v-for="item in roles" :key="item.id" type="button" role="radio" :aria-checked="identityId === item.id" :data-testid="`identity-${item.id}`" :disabled="busy || logoutPending" :class="['login-role', { selected: identityId === item.id }]" @click="identityId = item.id"><span class="role-icon"><UserOutlined /></span><span><strong>{{ item.label }}</strong><small>{{ item.description }}</small></span><span class="role-check"><CheckOutlined v-if="identityId === item.id" /></span></button></div>
        <a-button v-if="logoutPending" type="primary" size="large" block :loading="busy" class="login-submit" data-testid="retry-logout" @click="emit('retryLogout')">重试退出</a-button>
        <a-button v-else html-type="submit" type="primary" size="large" block :loading="busy" class="login-submit" data-testid="login-submit">进入工作台<ArrowRightOutlined /></a-button>
        <div class="login-privacy"><SafetyCertificateOutlined /><span>仅可访问所选企业内的授权业务数据</span></div>
      </form>
      <footer class="login-legal">易数链动企业管理平台</footer>
    </section>
  </main>
</template>
