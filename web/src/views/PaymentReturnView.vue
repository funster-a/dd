<script setup lang="ts">
import { onMounted } from 'vue'
import { useRoute, useRouter } from 'vue-router'

// Провайдер возвращает покупателя сюда с order_id. Итог оплаты платформа
// узнаёт из вебхука, поэтому статус показывает страница заказа.
const route = useRoute()
const router = useRouter()

onMounted(() => {
  const id = route.query.order_id
  if (typeof id === 'string' && /^[0-9a-f-]{36}$/i.test(id)) router.replace({ name: 'order', params: { id }, query: { paid: '1' } })
  else router.replace('/me')
})
</script>

<template>
  <div class="page" style="padding-top: 96px">
    <p class="eyebrow">Возвращаемся от платёжного провайдера…</p>
  </div>
</template>
