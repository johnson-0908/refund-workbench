<script setup lang="ts">
import logoUrl from '../assets/yishuld-logo.png'

withDefaults(defineProps<{ variant?: 'login' | 'sidebar' }>(), { variant: 'login' })
</script>

<template>
  <div :class="['brand-logo', `brand-logo--${variant}`]">
    <img :src="logoUrl" alt="易数链动" width="1825" height="862" />
  </div>
</template>

<style scoped>
.brand-logo {
  box-sizing: border-box;
  width: 228px;
  max-width: 100%;
  flex-shrink: 1;
  padding: 12px;
  border: 1px solid #e5ece9;
  border-radius: 10px;
  background: #fff;
}
.brand-logo--sidebar { width: 164px; padding: 8px; border-radius: 7px; }
.brand-logo img { display: block; width: 100%; height: auto; object-fit: contain; }
@media (max-width: 1000px) {
  .brand-logo--sidebar { padding: 4px; border-radius: 5px; }
}
</style>
