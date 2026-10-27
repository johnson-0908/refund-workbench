<script setup lang="ts">
import { computed } from 'vue'
import { statusInfo } from '../lib/format'
import type { RefundStatus } from '../lib/types'
const props = defineProps<{ status: RefundStatus | null }>()
const info = computed(() => props.status ? statusInfo[props.status] : null)
</script>

<template>
  <a-tag v-if="info" :color="info.color" class="status-tag"><span class="tag-dot" />{{ info.label }}</a-tag>
  <a-tag v-else class="status-tag status-neutral"><span class="tag-dot" />未申请</a-tag>
</template>
