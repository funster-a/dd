<script setup lang="ts">
import { computed } from 'vue'
import qrcode from 'qrcode-generator'

// QR-код как SVG: чёткий на любом экране и при печати. Тёмные модули —
// одним path, без сотен отдельных элементов.
const props = defineProps<{ value: string; label: string }>()

const qr = computed(() => {
  const q = qrcode(0, 'M')
  q.addData(props.value)
  q.make()
  const n = q.getModuleCount()
  let d = ''
  for (let r = 0; r < n; r++) for (let c = 0; c < n; c++) if (q.isDark(r, c)) d += `M${c + 4} ${r + 4}h1v1h-1z`
  return { size: n + 8, d }
})
</script>

<template>
  <svg :viewBox="`0 0 ${qr.size} ${qr.size}`" role="img" :aria-label="label" class="qr" shape-rendering="crispEdges">
    <rect :width="qr.size" :height="qr.size" fill="#fff" />
    <path :d="qr.d" fill="#000" />
  </svg>
</template>

<style scoped>
.qr {
  display: block;
  width: 100%;
  height: auto;
  border-radius: var(--radius-sm);
}
</style>
