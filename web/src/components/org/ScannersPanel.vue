<script setup lang="ts">
import { onMounted, ref } from 'vue'
import QrCode from '@/components/QrCode.vue'
import { orgApi } from '@/api/client'
import type { ScannerLink } from '@/api/types'
import { useToast } from '@/composables/toast'
import { explain } from '@/lib/eventForm'

// Ссылки сканера для контролёров (ADR 013): по ссылке открывается экран
// проверки билетов, без входа и паролей. Токен виден один раз — при создании.
const props = defineProps<{ eventId: string; locked: boolean }>()
const toast = useToast()
const links = ref<ScannerLink[] | null>(null)
const name = ref('')
const created = ref<ScannerLink | null>(null)
const busy = ref(false)

async function load() {
  links.value = await orgApi.scanners(props.eventId).catch(() => [])
}
onMounted(load)

async function create() {
  if (!name.value.trim()) return
  busy.value = true
  try {
    created.value = await orgApi.createScanner(props.eventId, name.value.trim())
    name.value = ''
    await load()
  } catch (e) {
    toast.show(explain(e, 'Не удалось создать ссылку'), 'error')
  } finally {
    busy.value = false
  }
}

async function revoke(l: ScannerLink) {
  if (!confirm(`Отозвать ссылку «${l.name}»? Сканер на этом устройстве перестанет работать.`)) return
  try {
    await orgApi.revokeScanner(l.id)
    if (created.value?.id === l.id) created.value = null
    await load()
    toast.show('Ссылка отозвана')
  } catch {
    toast.show('Не удалось отозвать ссылку', 'error')
  }
}

async function copy(url: string) {
  try {
    await navigator.clipboard.writeText(url)
    toast.show('Ссылка скопирована', 'ok')
  } catch {
    toast.show('Скопируйте ссылку вручную')
  }
}

const date = (iso: string) => new Intl.DateTimeFormat('ru-RU', { day: 'numeric', month: 'short', hour: '2-digit', minute: '2-digit' }).format(new Date(iso))
</script>

<template>
  <div class="scanners">
    <p class="lead">
      Контролёр открывает ссылку на своём телефоне и сканирует QR-коды билетов камерой. Без интернета сканер продолжает работать по
      загруженному списку и отправляет проходы, когда связь вернётся. Каждому входу — своя ссылка: её можно отозвать отдельно.
    </p>

    <form v-if="!locked" class="create" @submit.prevent="create">
      <input v-model="name" class="input" maxlength="100" placeholder="Название, например «Вход А» или «Касса»" aria-label="Название ссылки" />
      <button class="btn btn--accent" :disabled="busy || !name.trim()">Создать ссылку</button>
    </form>

    <section v-if="created?.url" class="fresh" aria-live="polite">
      <div class="fresh__qr"><QrCode :value="created.url" :label="`QR-код ссылки сканера ${created.name}`" /></div>
      <div class="fresh__body">
        <p class="eyebrow">Новая ссылка · {{ created.name }}</p>
        <p class="fresh__title">Отсканируйте телефоном контролёра</p>
        <p class="muted">Ссылка показывается один раз. Если потеряете — отзовите её и создайте новую.</p>
        <code class="url mono">{{ created.url }}</code>
        <div class="fresh__actions">
          <button class="btn btn--sm" type="button" @click="copy(created.url!)">Скопировать</button>
          <a class="btn btn--sm btn--ghost" :href="created.url" target="_blank" rel="noopener">Открыть здесь ↗</a>
        </div>
      </div>
    </section>

    <section>
      <h3 class="h3">Ссылки</h3>
      <div v-if="!links" class="skeleton" style="height: 80px"></div>
      <p v-else-if="links.length === 0" class="muted">Пока ни одной.</p>
      <ul v-else class="list">
        <li v-for="l in links" :key="l.id" :class="{ revoked: l.revoked_at }">
          <span class="list__name">{{ l.name }}</span>
          <span class="muted mono">создана {{ date(l.created_at) }}</span>
          <span v-if="l.revoked_at" class="chip chip--cancelled">Отозвана</span>
          <button v-else type="button" class="btn btn--sm btn--ghost" @click="revoke(l)">Отозвать</button>
        </li>
      </ul>
    </section>
  </div>
</template>

<style scoped>
.scanners {
  display: grid;
  gap: var(--space-5);
}
.lead {
  color: var(--ink-2);
  max-width: 70ch;
}
.create {
  display: flex;
  gap: var(--space-3);
  flex-wrap: wrap;
}
.create .input {
  flex: 1;
  min-width: 240px;
}
.fresh {
  display: grid;
  grid-template-columns: 180px 1fr;
  gap: var(--space-5);
  padding: var(--space-5);
  border-radius: var(--radius-lg);
  background: var(--ink);
  color: var(--paper);
}
.fresh .muted {
  color: color-mix(in srgb, var(--paper) 70%, transparent);
}
.fresh__body {
  display: grid;
  gap: var(--space-2);
  align-content: center;
  min-width: 0;
}
.fresh__title {
  font-size: var(--text-xl);
  font-weight: 700;
  letter-spacing: -0.02em;
}
.url {
  display: block;
  padding: var(--space-2) var(--space-3);
  border-radius: var(--radius-sm);
  background: color-mix(in srgb, var(--paper) 10%, transparent);
  font-size: var(--text-xs);
  overflow-wrap: anywhere;
}
.fresh__actions {
  display: flex;
  gap: var(--space-2);
  flex-wrap: wrap;
}
.fresh .btn {
  background: var(--paper);
  color: var(--ink);
}
.fresh .btn--ghost {
  background: transparent;
  color: var(--paper);
  border-color: color-mix(in srgb, var(--paper) 40%, transparent);
}
@media (max-width: 560px) {
  .fresh {
    grid-template-columns: 1fr;
  }
  .fresh__qr {
    max-width: 220px;
  }
}
.h3 {
  font-size: var(--text-lg);
  margin-bottom: var(--space-3);
}
.muted {
  color: var(--ink-2);
  font-size: var(--text-sm);
}
.list {
  list-style: none;
  margin: 0;
  padding: 0;
  border-top: 1px solid var(--ink);
}
.list li {
  display: flex;
  align-items: center;
  gap: var(--space-4);
  padding: var(--space-3) 0;
  border-bottom: 1px solid var(--line);
}
.list__name {
  font-weight: 600;
  margin-right: auto;
}
.list li.revoked {
  opacity: 0.55;
}
</style>
