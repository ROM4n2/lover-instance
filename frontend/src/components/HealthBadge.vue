<template>
  <div class="health" :class="stateClass" :title="hint">
    <span class="dot"></span>
    <span class="text">{{ label }}</span>
    <span v-if="degraded && hint" class="hint">· {{ hint }}</span>
  </div>
</template>

<script setup>
import { computed } from 'vue'

const props = defineProps({
  health: { type: Object, default: null },
})

// Degraded is the default until the first answer: a badge that claims "ok"
// before the health check has completed is a badge that lies for the first
// second. "degraded" is a real state the server reports (design §Task 1),
// never a failed request — the endpoint answers 200 even with NLP down.
const degraded = computed(() => !props.health || props.health.status !== 'ok')

const stateClass = computed(() => (degraded.value ? 'degraded' : 'ok'))

const label = computed(() => {
  if (!props.health) return '连接中…'
  return props.health.status === 'ok' ? '服务正常' : '服务降级'
})

// The hint names the part that is missing, because "degraded" alone does not
// tell the user whether to start the Python service or check the database.
const hint = computed(() => {
  const health = props.health
  if (!health) return ''
  if (health.db === 'error') return '数据库异常'
  if (health.nlp && !health.nlp.reachable) return '情感服务未启动'
  if (health.nlp && health.nlp.status && health.nlp.status !== 'ready') return '情感服务加载中'
  return ''
})
</script>
