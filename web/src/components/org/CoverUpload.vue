<script setup lang="ts">
import { ref } from 'vue'
import { orgApi, uploadFile } from '@/api/client'
import type { OrgEvent } from '@/api/types'
import { useToast } from '@/composables/toast'
import { explain } from '@/lib/eventForm'

// Обложка события: картинка обязательна, короткое видео — по желанию.
// Файл уходит прямо в хранилище по подписанной ссылке (ADR 009), сервер
// потом проверяет, что он загружен, нужного типа и размера.
const props = defineProps<{ event: OrgEvent; locked: boolean }>()
const emit = defineEmits<{ saved: [event: OrgEvent] }>()
const toast = useToast()

const RULES = {
  cover_image: { types: ['image/jpeg', 'image/png', 'image/webp'], max: 5 << 20, label: 'JPG, PNG или WebP до 5 МБ' },
  cover_video: { types: ['video/mp4', 'video/webm'], max: 15 << 20, label: 'MP4 или WebM до 15 МБ, без звука' },
} as const
type Kind = keyof typeof RULES

const progress = ref<Record<Kind, number | null>>({ cover_image: null, cover_video: null })
const drag = ref<Kind | null>(null)
const error = ref('')

async function upload(kind: Kind, file: File | undefined) {
  if (!file || props.locked) return
  error.value = ''
  const rule = RULES[kind]
  if (!(rule.types as readonly string[]).includes(file.type)) return void (error.value = `Формат не подходит: ${rule.label}`)
  if (file.size > rule.max) return void (error.value = `Файл больше лимита: ${rule.label}`)
  if (kind === 'cover_video' && !props.event.cover_image_key) return void (error.value = 'Сначала картинка: она показывается, пока видео грузится')
  progress.value[kind] = 0
  try {
    const target = await orgApi.createUpload(props.event.id, kind, file)
    await uploadFile(target, file, (p) => (progress.value[kind] = p))
    const image = kind === 'cover_image' ? target.key : props.event.cover_image_key!
    const video = kind === 'cover_video' ? target.key : props.event.cover_video_key
    const e = await orgApi.setMedia(props.event.id, image, video)
    toast.show(kind === 'cover_image' ? 'Обложка загружена' : 'Видео загружено', 'ok')
    emit('saved', e)
  } catch (e) {
    error.value = explain(e, 'Не удалось загрузить файл. Проверьте соединение и попробуйте ещё раз')
  } finally {
    progress.value[kind] = null
  }
}

async function removeVideo() {
  try {
    emit('saved', await orgApi.setMedia(props.event.id, props.event.cover_image_key!, null))
  } catch (e) {
    error.value = explain(e)
  }
}

function onDrop(kind: Kind, e: DragEvent) {
  drag.value = null
  upload(kind, e.dataTransfer?.files[0])
}
</script>

<template>
  <div class="cover">
    <div class="cover__grid">
      <label
        class="drop drop--image"
        :class="{ 'is-drag': drag === 'cover_image', 'is-locked': locked }"
        @dragover.prevent="drag = 'cover_image'"
        @dragleave="drag = null"
        @drop.prevent="onDrop('cover_image', $event)"
      >
        <input type="file" class="visually-hidden" :accept="RULES.cover_image.types.join(',')" :disabled="locked" @change="upload('cover_image', ($event.target as HTMLInputElement).files?.[0])" />
        <img v-if="event.cover_image_url" :src="event.cover_image_url" alt="Обложка события" />
        <span class="drop__body" :class="{ 'drop__body--over': event.cover_image_url }">
          <b>{{ event.cover_image_url ? 'Заменить обложку' : 'Обложка' }}</b>
          <span>{{ RULES.cover_image.label }}. Лучше вертикальная, 4:5</span>
        </span>
        <span v-if="progress.cover_image !== null" class="progress" :style="{ '--p': Math.round(progress.cover_image * 100) + '%' }"></span>
      </label>

      <div class="side">
        <label
          class="drop drop--video"
          :class="{ 'is-drag': drag === 'cover_video', 'is-locked': locked }"
          @dragover.prevent="drag = 'cover_video'"
          @dragleave="drag = null"
          @drop.prevent="onDrop('cover_video', $event)"
        >
          <input type="file" class="visually-hidden" :accept="RULES.cover_video.types.join(',')" :disabled="locked" @change="upload('cover_video', ($event.target as HTMLInputElement).files?.[0])" />
          <video v-if="event.cover_video_url" :src="event.cover_video_url" muted loop autoplay playsinline></video>
          <span class="drop__body" :class="{ 'drop__body--over': event.cover_video_url }">
            <b>{{ event.cover_video_url ? 'Заменить видео' : 'Видео-обложка' }} <span class="opt">необязательно</span></b>
            <span>{{ RULES.cover_video.label }}</span>
          </span>
          <span v-if="progress.cover_video !== null" class="progress" :style="{ '--p': Math.round(progress.cover_video * 100) + '%' }"></span>
        </label>
        <button v-if="event.cover_video_key && !locked" type="button" class="link" @click="removeVideo">Убрать видео</button>
        <p class="tip">Обложку видно в афише и наверху страницы события. Текст на ней не обязателен — название покупатели и так видят рядом.</p>
      </div>
    </div>
    <p v-if="error" class="error-text" role="alert">{{ error }}</p>
  </div>
</template>

<style scoped>
.cover {
  display: grid;
  gap: var(--space-3);
}
.cover__grid {
  display: grid;
  gap: var(--space-4);
  grid-template-columns: minmax(0, 320px) minmax(0, 1fr);
}
@media (max-width: 700px) {
  .cover__grid {
    grid-template-columns: 1fr;
  }
}
.drop {
  position: relative;
  display: grid;
  place-items: center;
  overflow: hidden;
  border: 1.5px dashed var(--line-strong);
  border-radius: var(--radius-lg);
  background: var(--paper-2);
  cursor: pointer;
  text-align: center;
  transition:
    border-color 0.15s,
    background 0.15s;
}
.drop:hover,
.drop.is-drag {
  border-color: var(--ink);
  background: var(--paper-3);
}
.drop:focus-within {
  outline: 2px solid var(--accent);
  outline-offset: 2px;
}
.drop.is-locked {
  cursor: default;
}
.drop--image {
  aspect-ratio: 4 / 5;
}
.drop--video {
  aspect-ratio: 16 / 9;
}
.drop img,
.drop video {
  position: absolute;
  inset: 0;
  width: 100%;
  height: 100%;
  object-fit: cover;
}
.drop__body {
  position: relative;
  display: grid;
  gap: 4px;
  padding: var(--space-4);
  font-size: var(--text-sm);
  color: var(--ink-2);
}
.drop__body b {
  color: var(--ink);
  font-size: var(--text-md);
}
.drop__body--over {
  align-self: end;
  width: 100%;
  background: linear-gradient(transparent, rgba(0, 0, 0, 0.7));
  color: rgba(255, 255, 255, 0.8);
  opacity: 0;
  transition: opacity 0.15s;
}
.drop__body--over b {
  color: #fff;
}
.drop:hover .drop__body--over,
.drop:focus-within .drop__body--over {
  opacity: 1;
}
.opt {
  font-weight: 400;
  font-size: var(--text-xs);
  color: inherit;
  opacity: 0.7;
}
.progress {
  position: absolute;
  left: 0;
  right: 0;
  bottom: 0;
  height: 4px;
  background: linear-gradient(90deg, var(--accent) var(--p), transparent var(--p));
}
.side {
  display: grid;
  gap: var(--space-3);
  align-content: start;
}
.tip {
  font-size: var(--text-sm);
  color: var(--ink-2);
}
.link {
  justify-self: start;
  background: none;
  border: 0;
  padding: 0;
  font: inherit;
  font-size: var(--text-sm);
  color: var(--danger);
  cursor: pointer;
  text-decoration: underline;
}
</style>
