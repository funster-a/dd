<script setup lang="ts">
import { computed, onBeforeUnmount, onMounted, ref, watch } from 'vue'
import { useScanner } from '@/composables/scanner'
import { CameraError, startReader, type QrReader } from '@/lib/qrReader'
import { OUTCOME_TEXT, placeText } from '@/lib/scanner'
import { time } from '@/lib/format'

// Экран контролёра: камера, крупный итог проверки, работа без сети.
const { state, stats, setLink, refreshManifest, scan, sync, forget, dismiss } = useScanner()

const video = ref<HTMLVideoElement | null>(null)
const cameraError = ref('')
const manualOpen = ref(false)
const manualCode = ref('')
let reader: QrReader | null = null
let lastCode = ''
let lastAt = 0
let timers: ReturnType<typeof setInterval>[] = []
let hideTimer: ReturnType<typeof setTimeout> | undefined
let wakeLock: { release(): Promise<void> } | null = null

const tz = computed(() => state.manifest?.event.timezone ?? 'UTC')
const shownText = computed(() => (state.shown ? OUTCOME_TEXT[state.shown.result] : null))

async function onCode(code: string) {
  // Билет, который остаётся в кадре, проверяется один раз: повторно — только
  // если его убрали из кадра хотя бы на 5 секунд или закрыли итог вручную.
  const now = Date.now()
  const same = code === lastCode && now - lastAt < 5000
  lastAt = now
  if (same) return
  lastCode = code
  await scan(code)
}

function closeResult() {
  lastCode = ''
  dismiss()
}

watch(
  () => state.shown?.id,
  () => {
    const s = state.shown
    if (!s) return
    feedback(OUTCOME_TEXT[s.result].tone)
    clearTimeout(hideTimer)
    hideTimer = setTimeout(dismiss, s.result === 'accepted' ? 1800 : 3500)
  },
)

// Звук и вибрация: контролёр не обязан смотреть на экран каждый раз.
let audio: AudioContext | null = null
function feedback(tone: 'ok' | 'warn' | 'bad') {
  try {
    navigator.vibrate?.(tone === 'ok' ? 60 : [120, 80, 120])
    audio ??= new AudioContext()
    const o = audio.createOscillator()
    const g = audio.createGain()
    o.frequency.value = tone === 'ok' ? 880 : tone === 'warn' ? 520 : 220
    o.type = tone === 'ok' ? 'sine' : 'square'
    g.gain.setValueAtTime(0.12, audio.currentTime)
    g.gain.exponentialRampToValueAtTime(0.001, audio.currentTime + (tone === 'ok' ? 0.15 : 0.35))
    o.connect(g).connect(audio.destination)
    o.start()
    o.stop(audio.currentTime + 0.4)
  } catch {
    // без звука
  }
}

async function startCamera() {
  cameraError.value = ''
  reader?.stop()
  if (!video.value) return
  try {
    reader = await startReader(video.value, onCode)
  } catch (e) {
    cameraError.value =
      e instanceof CameraError && e.reason === 'denied'
        ? 'Разрешите доступ к камере в настройках браузера'
        : e instanceof CameraError
          ? e.message
          : 'Не удалось включить камеру'
  }
}

function submitManual() {
  const c = manualCode.value.trim()
  if (!c) return
  manualCode.value = ''
  manualOpen.value = false
  scan(c)
}

function onOnline() {
  state.online = true
  sync().then(refreshManifest)
}
function onOffline() {
  state.online = false
}

// Манифест PWA — только для экрана сканера: его ставят на телефон контролёра.
function addManifest() {
  if (document.querySelector('link[rel="manifest"]')) return
  const l = document.createElement('link')
  l.rel = 'manifest'
  l.href = '/scanner.webmanifest'
  document.head.appendChild(l)
}

// Оболочка приложения в кэше service worker: экран открывается и без сети.
async function registerOffline() {
  if (!('serviceWorker' in navigator) || !import.meta.env.PROD) return
  try {
    const reg = await navigator.serviceWorker.register('/sw.js')
    const worker = reg.active ?? (await navigator.serviceWorker.ready).active
    const urls = ['/scan', ...performance.getEntriesByType('resource').map((r) => r.name).filter((u) => u.includes('/assets/'))]
    worker?.postMessage({ type: 'precache', urls })
  } catch {
    // без офлайн-оболочки сканер всё равно работает, пока вкладка открыта
  }
}

onMounted(async () => {
  addManifest()
  // Токен приходит во фрагменте ссылки (#…) и сразу убирается из адреса.
  const hash = location.hash.slice(1)
  if (hash) {
    await setLink(decodeURIComponent(hash))
    history.replaceState(null, '', '/scan')
  }
  if (!state.token) return
  await refreshManifest()
  await sync()
  await startCamera()
  // После камеры: в список файлов попадает и загруженный декодер QR.
  registerOffline()
  timers = [
    setInterval(() => state.online && refreshManifest(), 60_000),
    setInterval(() => sync(), 15_000),
  ]
  window.addEventListener('online', onOnline)
  window.addEventListener('offline', onOffline)
  try {
    wakeLock = await (navigator as Navigator & { wakeLock?: { request(t: 'screen'): Promise<{ release(): Promise<void> }> } }).wakeLock?.request('screen') ?? null
  } catch {
    // экран может погаснуть — не страшно
  }
})

onBeforeUnmount(() => {
  reader?.stop()
  timers.forEach(clearInterval)
  clearTimeout(hideTimer)
  window.removeEventListener('online', onOnline)
  window.removeEventListener('offline', onOffline)
  wakeLock?.release().catch(() => {})
})

function signOut() {
  if (state.queue.length && !confirm(`${state.queue.length} проходов ещё не отправлены. Всё равно выйти?`)) return
  reader?.stop()
  forget()
}
</script>

<template>
  <div class="scanner">
    <!-- Нет ссылки: сканер открыт не по ссылке организатора. -->
    <div v-if="!state.token" class="center">
      <span class="brand-mark"></span>
      <h1>Контроль входа</h1>
      <p>Откройте ссылку сканера, которую прислал организатор, — или отсканируйте её QR-код камерой телефона.</p>
    </div>

    <div v-else-if="state.revoked" class="center center--bad">
      <h1>Ссылка отозвана</h1>
      <p>Организатор отключил этот сканер. Попросите новую ссылку.</p>
      <button type="button" class="ghost" @click="forget">Закрыть</button>
    </div>

    <template v-else>
      <header class="top">
        <div class="top__event">
          <b>{{ state.manifest?.event.title ?? 'Загрузка…' }}</b>
          <span v-if="state.manifest" class="mono">
            {{ time(state.manifest.event.starts_at, tz) }} · {{ state.manifest.event.venue }}
          </span>
        </div>
        <span class="net" :class="state.online ? 'net--on' : 'net--off'" role="status">
          <i></i>{{ state.online ? 'онлайн' : 'без сети' }}
          <template v-if="state.queue.length"> · {{ state.queue.length }} в очереди</template>
        </span>
      </header>

      <div class="view">
        <video ref="video" class="view__video" muted playsinline></video>
        <div class="frame" aria-hidden="true"><i></i><i></i><i></i><i></i></div>
        <p v-if="cameraError" class="view__error">
          {{ cameraError }}
          <button type="button" class="ghost" @click="startCamera">Попробовать снова</button>
        </p>
        <p v-else class="view__hint">Наведите камеру на QR-код билета</p>
      </div>

      <footer class="bottom">
        <div class="count">
          <span class="count__n mono">{{ stats.used }}</span>
          <span class="count__of">из {{ stats.active }} прошли</span>
        </div>
        <button type="button" class="ghost" @click="manualOpen = true">Ввести код</button>
        <button type="button" class="ghost ghost--dim" @click="signOut">Выйти</button>
      </footer>
      <p v-if="state.mismatches" class="note">
        При синхронизации {{ state.mismatches }} офлайн-проход(ов) оказались повторными — билет прошёл на другом входе раньше.
      </p>

      <form v-if="manualOpen" class="manual" @submit.prevent="submitManual">
        <label for="manual-code">Ссылка или код билета</label>
        <input id="manual-code" v-model="manualCode" class="mono" autocomplete="off" autofocus placeholder="…/t/AaDyNV5ofs…" />
        <div class="manual__actions">
          <button class="solid">Проверить</button>
          <button type="button" class="ghost" @click="manualOpen = false">Отмена</button>
        </div>
      </form>

      <Transition name="pop">
        <button v-if="state.shown && shownText" :key="state.shown.id" type="button" class="result" :class="`result--${shownText.tone}`" @click="closeResult">
          <span class="result__icon" aria-hidden="true">{{ shownText.tone === 'ok' ? '✓' : shownText.tone === 'warn' ? '↺' : '×' }}</span>
          <span class="result__title" role="alert">{{ shownText.title }}</span>
          <span v-if="placeText(state.shown)" class="result__place">{{ placeText(state.shown) }}</span>
          <span v-if="state.shown.result === 'duplicate' && state.shown.firstAt" class="result__sub mono">
            первый проход в {{ time(state.shown.firstAt, tz) }}
          </span>
          <span v-if="state.shown.offline" class="result__sub mono">без сети · проверено по списку</span>
        </button>
      </Transition>
    </template>
  </div>
</template>

<style scoped>
.scanner {
  --bg: #0c0b0a;
  --fg: #f4efe6;
  --dim: #8f887c;
  --mark-bg: var(--bg);
  --mark: var(--accent);
  position: fixed;
  inset: 0;
  display: flex;
  flex-direction: column;
  background: var(--bg);
  color: var(--fg);
  overflow: hidden;
  user-select: none;
}
.center {
  margin: auto;
  max-width: 420px;
  padding: var(--space-5);
  display: grid;
  gap: var(--space-4);
  justify-items: center;
  text-align: center;
}
.center h1 {
  font-size: var(--text-2xl);
}
.center p {
  color: var(--dim);
}
.center--bad h1 {
  color: #ff6a55;
}
.top {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: var(--space-3);
  padding: calc(var(--space-3) + env(safe-area-inset-top)) var(--space-4) var(--space-3);
}
.top__event {
  display: grid;
  min-width: 0;
}
.top__event b {
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}
.top__event .mono {
  font-size: var(--text-xs);
  color: var(--dim);
}
.net {
  display: inline-flex;
  align-items: center;
  gap: 6px;
  flex: none;
  padding: 5px 10px;
  border-radius: 99px;
  font-size: var(--text-xs);
  background: #1d1b18;
}
.net i {
  width: 7px;
  height: 7px;
  border-radius: 50%;
  background: currentColor;
}
.net--on {
  color: #57c28b;
}
.net--off {
  color: #f0b44c;
}
.view {
  position: relative;
  flex: 1;
  min-height: 0;
  display: grid;
  place-items: center;
}
.view__video {
  position: absolute;
  inset: 0;
  width: 100%;
  height: 100%;
  object-fit: cover;
  opacity: 0.9;
}
.frame {
  position: relative;
  width: min(70vw, 52vh, 340px);
  aspect-ratio: 1;
  box-shadow: 0 0 0 100vmax rgba(12, 11, 10, 0.55);
  border-radius: 20px;
}
.frame i {
  position: absolute;
  width: 34px;
  height: 34px;
  border: 4px solid var(--fg);
}
.frame i:nth-child(1) {
  top: 0;
  left: 0;
  border-right: 0;
  border-bottom: 0;
  border-top-left-radius: 20px;
}
.frame i:nth-child(2) {
  top: 0;
  right: 0;
  border-left: 0;
  border-bottom: 0;
  border-top-right-radius: 20px;
}
.frame i:nth-child(3) {
  bottom: 0;
  left: 0;
  border-right: 0;
  border-top: 0;
  border-bottom-left-radius: 20px;
}
.frame i:nth-child(4) {
  bottom: 0;
  right: 0;
  border-left: 0;
  border-top: 0;
  border-bottom-right-radius: 20px;
}
.view__hint,
.view__error {
  position: absolute;
  bottom: var(--space-5);
  left: var(--space-4);
  right: var(--space-4);
  text-align: center;
  font-size: var(--text-sm);
  color: var(--fg);
  display: grid;
  gap: var(--space-3);
  justify-items: center;
}
.view__error {
  top: 50%;
  bottom: auto;
  transform: translateY(-50%);
  padding: var(--space-5);
  background: rgba(12, 11, 10, 0.85);
  border-radius: var(--radius-lg);
}
.bottom {
  display: flex;
  align-items: center;
  gap: var(--space-2);
  padding: var(--space-3) var(--space-4) calc(var(--space-3) + env(safe-area-inset-bottom));
}
.count {
  display: grid;
  margin-right: auto;
}
.count__n {
  font-size: var(--text-2xl);
  font-weight: 600;
  line-height: 1;
}
.count__of {
  font-size: var(--text-xs);
  color: var(--dim);
}
.ghost,
.solid {
  min-height: 44px;
  padding: 0 var(--space-4);
  border-radius: 99px;
  font: inherit;
  font-weight: 600;
  font-size: var(--text-sm);
  cursor: pointer;
}
.ghost {
  background: transparent;
  color: var(--fg);
  border: 1px solid #3a3631;
}
.ghost--dim {
  color: var(--dim);
}
.solid {
  background: var(--fg);
  color: var(--bg);
  border: 0;
}
.note {
  padding: 0 var(--space-4) var(--space-3);
  font-size: var(--text-xs);
  color: #f0b44c;
}
.manual {
  position: absolute;
  left: 0;
  right: 0;
  bottom: 0;
  z-index: 5;
  display: grid;
  gap: var(--space-3);
  padding: var(--space-5) var(--space-4) calc(var(--space-5) + env(safe-area-inset-bottom));
  background: #1a1815;
  border-radius: 20px 20px 0 0;
}
.manual label {
  font-size: var(--text-sm);
  color: var(--dim);
}
.manual input {
  min-height: 48px;
  padding: 0 var(--space-3);
  border-radius: var(--radius);
  border: 1px solid #3a3631;
  background: var(--bg);
  color: var(--fg);
  font-size: var(--text-md);
}
.manual__actions {
  display: flex;
  gap: var(--space-2);
}
.result {
  position: absolute;
  inset: 0;
  z-index: 10;
  display: grid;
  align-content: center;
  justify-items: center;
  gap: var(--space-3);
  padding: var(--space-5);
  border: 0;
  font: inherit;
  text-align: center;
  cursor: pointer;
  color: #fff;
}
.result--ok {
  background: #178a52;
}
.result--warn {
  background: #c7860f;
  color: #1a1204;
}
.result--bad {
  background: #c8321f;
}
.result__icon {
  width: 120px;
  height: 120px;
  display: grid;
  place-items: center;
  border-radius: 50%;
  border: 6px solid currentColor;
  font-size: 64px;
  font-weight: 700;
  line-height: 1;
}
.result__title {
  font-size: clamp(2.4rem, 1.6rem + 5vw, 4rem);
  font-weight: 800;
  letter-spacing: -0.04em;
  line-height: 1;
}
.result__place {
  font-size: var(--text-xl);
  font-weight: 600;
}
.result__sub {
  font-size: var(--text-sm);
  opacity: 0.85;
}
.pop-enter-active {
  transition:
    opacity 0.12s,
    transform 0.18s var(--ease);
}
.pop-leave-active {
  transition: opacity 0.2s;
}
.pop-enter-from {
  opacity: 0;
  transform: scale(1.04);
}
.pop-leave-to {
  opacity: 0;
}
@media (prefers-reduced-motion: reduce) {
  .pop-enter-active,
  .pop-leave-active {
    transition: none;
  }
}
</style>
