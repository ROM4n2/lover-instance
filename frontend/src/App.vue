<template>
  <div class="app">
    <header class="topbar">
      <div class="brand">
        <h1>聊天关系分析面板</h1>
        <div v-if="currentConversation" class="partner-title">
          伴侣：<span class="partner-name">{{ currentConversation.display_name }}</span>
        </div>
      </div>

      <div class="talker-field">
        <span class="field-label">分析会话：</span>
        <el-select
          v-if="conversations.length > 0"
          v-model="selectedTalker"
          size="small"
          placeholder="选择聊天对象"
          style="width: 200px"
          @change="handleSelectConversation"
        >
          <el-option
            v-for="item in conversations"
            :key="item.talker"
            :label="`${item.display_name} (${formatCount(item.message_count)}条)`"
            :value="item.talker"
          >
            <span style="float: left">{{ item.display_name }}</span>
            <span style="float: right; color: var(--el-text-color-secondary); font-size: 12px">
              {{ formatCount(item.message_count) }}条
            </span>
          </el-option>
        </el-select>
        <span v-else class="empty-hint">暂无会话</span>

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
      </div>

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
        <HighlightsTimeline
          v-else-if="view === 'highlights'"
          :talker="talker"
          :overview="overview"
          :partner-name="currentConversation ? currentConversation.display_name : ''"
        />
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
const selectedTalker = ref('')
const conversations = ref([])

const currentConversation = computed(() => {
  return conversations.value.find(c => c.talker === talker.value) || null
})

function formatCount(num) {
  if (!num) return '0'
  return num >= 10000 ? (num / 10000).toFixed(1) + 'w' : num
}

async function loadConversations() {
  try {
    const res = await api.getConversations()
    conversations.value = res.conversations || []
    if (conversations.value.length > 0) {
      if (!talker.value) {
        const main = conversations.value.find(c => c.is_main) || conversations.value[0]
        talker.value = main.talker
        selectedTalker.value = main.talker
        writeState({ view: view.value, talker: talker.value }, { push: false })
      } else {
        selectedTalker.value = talker.value
      }
    }
  } catch (err) {
    console.error('Failed to load conversations:', err)
  }
}

function handleSelectConversation(val) {
  talker.value = val
  selectedTalker.value = val
  writeState({ view: view.value, talker: val }, { push: true })
  loadOverview()
}

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
      writeState({ view: view.value, talker: talker.value }, { push: false })
    }
    syncProgressPolling()
  } catch (error) {
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
  await Promise.all([loadHealth(), loadConversations(), loadOverview({ silent: true })])
}

function switchView(next) {
  view.value = normalizeView(next)
  writeState({ view: view.value, talker: talker.value }, { push: true })
}

function onPopState() {
  const state = readState()
  view.value = state.view
  talker.value = state.talker
  selectedTalker.value = state.talker
  loadOverview()
}

onMounted(async () => {
  const initial = readState()
  view.value = initial.view
  talker.value = initial.talker
  if (initial.talker) {
    selectedTalker.value = initial.talker
  }

  writeState({ view: view.value, talker: talker.value }, { push: false })

  loadHealth()
  await loadConversations()
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
