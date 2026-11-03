<template>
  <div class="app">
    <header class="topbar">
      <div class="brand">
        <h1>聊天关系分析面板</h1>
        <div v-if="overview && overview.talker_hash" class="hash">会话指纹 {{ overview.talker_hash }}</div>
      </div>

      <label class="talker-field">
        会话
        <el-input
          v-model="talkerInput"
          size="small"
          placeholder="留空用默认会话"
          style="width: 160px"
          @keyup.enter="applyTalker"
          @blur="applyTalker"
        />
        <el-button
          type="primary"
          plain
          size="small"
          :loading="syncing"
          style="margin-left: 10px"
          @click="handleSyncSidecar"
        >
          ⚡ 同步微信记录
        </el-button>
      </label>

      <nav class="tabs">
        <button
          v-for="tab in tabs"
          :key="tab.key"
          :class="['tab', { active: view === tab.key }]"
          @click="switchView(tab.key)"
        >
          {{ tab.label }}
        </button>
      </nav>

      <HealthBadge :health="health" />
    </header>

    <main class="content">
      <EmptyOnboarding
        v-if="isEmptyDB"
        @imported="refreshAll"
      />
      <template v-else>
        <el-alert
          v-if="overviewError"
          :title="overviewError.message || '无法读取概览数据'"
          :description="overviewError.hint || ''"
          type="error"
          show-icon
          :closable="false"
          style="margin-bottom: 14px"
        />

        <Overview v-if="view === 'overview'" :overview="overview" />
        <HighlightsTimeline v-else-if="view === 'highlights'" :talker="talker" :overview="overview" />
        <Sentiment v-else-if="view === 'sentiment'" :overview="overview" :analysis="analysis" />
        <Jobs
          v-else-if="view === 'jobs'"
          :health="health"
          :talker="talker"
          :overview="overview"
          @refresh="refreshAll"
        />
      </template>
    </main>
  </div>
</template>

<script setup>
import { computed, onMounted, onUnmounted, ref } from 'vue'
import { ElMessage, ElMessageBox } from 'element-plus'
import * as api from './api'
import { normalizeView, readState, writeState } from './useUrlState'
import HealthBadge from './components/HealthBadge.vue'
import HighlightsTimeline from './components/HighlightsTimeline.vue'
import EmptyOnboarding from './components/EmptyOnboarding.vue'
import Overview from './views/Overview.vue'
import Sentiment from './views/Sentiment.vue'
import Jobs from './views/Jobs.vue'

const tabs = [
  { key: 'overview', label: '概览' },
  { key: 'highlights', label: '高光' },
  { key: 'sentiment', label: '情感' },
  { key: 'jobs', label: '任务' },
]

const view = ref('overview')
const talker = ref('')
const talkerInput = ref('')

const health = ref(null)
const overview = ref(null)
const overviewError = ref(null)
const syncing = ref(false)

async function handleSyncSidecar() {
  syncing.value = true
  try {
    const res = await api.syncSidecar(talker.value)
    ElMessage.success(`同步完成！解析 ${res.parsed || 0} 条消息，新增 ${res.inserted || 0} 条`)
    await refreshAll()
  } catch (err) {
    if (err.code === 'sidecar_missing') {
      ElMessageBox.alert(
        '<div style=\"line-height: 1.6\">' +
        '依据本地隐私安全规范，微信数据提取需由独立的本地小助手完成：<br><br>' +
        '<b>1.</b> 从 Releases 下载独立助手 <code>chatlog-keeper.exe</code><br>' +
        '<b>2.</b> 将它放入与 <code>lover.exe</code> <b>相同的文件夹</b><br>' +
        '<b>3.</b> 电脑登录微信，再次点击【⚡ 同步微信记录】即可全自动同步！</div>',
        '💡 微信提取助手未就绪',
        {
          dangerouslyUseHTMLString: true,
          confirmButtonText: '我知道了',
          type: 'info'
        }
      )
    } else {
      ElMessage.error(err.message || '微信同步失败：请确保电脑已登录微信')
    }
  } finally {
    syncing.value = false
  }
}

const isEmptyDB = computed(() => {
  return !overview.value && overviewError.value && overviewError.value.code === 'empty_database'
})

let healthTimer = null
let progressTimer = null

// analysis is the half-finished gate the views read. It defaults to "not
// running" so the first paint (before overview arrives) never draws a gate for
// a run that does not exist.
const analysis = computed(() => {
  const value = overview.value && overview.value.analysis
  return value || { running: false, done: 0, total: 0 }
})

async function loadHealth() {
  try {
    health.value = await api.getHealth()
  } catch {
    // A failed poll keeps the previous answer; the next tick tries again. The
    // badge is not the place to raise a network error.
  }
}

async function loadOverview({ silent = false } = {}) {
  if (!silent) overviewError.value = null
  try {
    const data = await api.getOverview(talker.value)
    overview.value = data
    if (data && data.talker_hash && !talker.value) {
      talker.value = data.talker_hash
      talkerInput.value = data.talker_hash
      writeState({ view: view.value, talker: talker.value }, { push: false })
    }
    syncProgressPolling()
  } catch (error) {
    if (error.code === 'talker_required' && !talker.value && error.hint) {
      const match = error.hint.match(/\?talker=:\s*([a-f0-9]{16})/)
      if (match && match[1]) {
        talker.value = match[1]
        talkerInput.value = match[1]
        writeState({ view: view.value, talker: talker.value }, { push: false })
        return loadOverview({ silent })
      }
    }
    if (!silent) overviewError.value = error
  }
}

// Polling rhythm. The health badge is every 5s (Task 6). Progress, while a run
// is active, is every 2s: fast enough that a moving bar reads as live, slow
// enough not to hammer the single database connection the worker also uses.
const HEALTH_INTERVAL = 5000
const PROGRESS_INTERVAL = 2000

function syncProgressPolling() {
  if (analysis.value.running) startProgressPolling()
  else stopProgressPolling()
}

function startProgressPolling() {
  if (progressTimer) return
  progressTimer = setInterval(tickProgress, PROGRESS_INTERVAL)
}

function stopProgressPolling() {
  if (progressTimer) {
    clearInterval(progressTimer)
    progressTimer = null
  }
}

// tickProgress asks the job endpoint for absolute progress while a run is
// active. When the run reaches a terminal status the gate is lifted and the
// overview is reloaded once so the views can draw the finished data.
async function tickProgress() {
  const job = health.value && health.value.job
  try {
    if (job && job.id) {
      const current = await api.getJob(job.id)
      const active = current.status === 'queued' || current.status === 'running'
      if (overview.value) {
        overview.value = {
          ...overview.value,
          analysis: { running: active, done: current.done, total: current.total },
        }
      }
      if (!active) {
        stopProgressPolling()
        await refreshAll()
      }
      return
    }

    // No job id yet (health has not caught up): fall back to the overview,
    // which carries analysis.done/total for the conversation's own slot.
    await loadOverview({ silent: true })
    if (!analysis.value.running) stopProgressPolling()
  } catch {
    // Transient failure: leave the timer running so the next tick retries.
  }
}

async function refreshAll() {
  await Promise.all([loadHealth(), loadOverview({ silent: true })])
}

function switchView(next) {
  view.value = normalizeView(next)
  writeState({ view: view.value, talker: talker.value }, { push: true })
}

function applyTalker() {
  const value = (talkerInput.value || '').trim()
  if (value === talker.value) return
  talker.value = value
  writeState({ view: view.value, talker: talker.value }, { push: true })
  loadOverview()
}

function onPopState() {
  const state = readState()
  view.value = state.view
  talker.value = state.talker
  talkerInput.value = state.talker
  loadOverview()
}

onMounted(() => {
  const initial = readState()
  view.value = initial.view
  talker.value = initial.talker
  talkerInput.value = initial.talker

  // Normalise the URL once so every load has a concrete ?view= and a bookmark
  // lands on the same tab. replace, not push: this is not a navigation.
  writeState({ view: view.value, talker: talker.value }, { push: false })

  loadHealth()
  loadOverview()
  healthTimer = setInterval(loadHealth, HEALTH_INTERVAL)
  window.addEventListener('popstate', onPopState)
})

onUnmounted(() => {
  if (healthTimer) clearInterval(healthTimer)
  stopProgressPolling()
  window.removeEventListener('popstate', onPopState)
})
</script>
