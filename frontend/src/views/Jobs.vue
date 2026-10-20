<template>
  <div class="view">
    <section class="panel">
      <h2>开始分析</h2>
      <p class="note">
        对当前会话的情感打分。同一会话同时只允许一个任务（单飞），已有任务时再点开始会得到既有的任务号。
      </p>
      <div class="controls">
        <el-select v-model="modelVersion" clearable placeholder="默认模型版本" style="width: 240px">
          <el-option v-for="version in versions" :key="version" :label="version" :value="version" />
        </el-select>
        <el-button type="primary" :loading="starting" :disabled="isActive" @click="start">
          开始分析
        </el-button>
        <span v-if="!versions.length" class="note">情感服务未提供可选模型版本，将使用默认版本。</span>
      </div>
      <el-alert
        v-if="message"
        :title="message.title"
        :description="message.detail"
        :type="message.type"
        show-icon
        :closable="false"
        style="margin-top: 12px"
      />
    </section>

    <section class="panel">
      <h2>当前任务</h2>
      <div v-if="activeJob">
        <p>
          状态：<strong>{{ statusLabel(activeJob.status) }}</strong>
          <span class="counter">（#{{ activeJob.id }}）</span>
        </p>
        <el-progress :percentage="percent" :stroke-width="16" />
        <p class="counter">{{ activeJob.done }} / {{ activeJob.total }} 条已打分</p>
        <div class="controls">
          <el-button v-if="isActive" type="danger" plain :loading="canceling" @click="cancel">
            取消任务
          </el-button>
          <el-button @click="$emit('refresh')">刷新状态</el-button>
        </div>
      </div>
      <el-empty v-else description="当前没有进行中的任务" />
    </section>

    <section class="panel">
      <h2>导出报告</h2>
      <p class="note">生成一份自包含的 HTML 报告到本地导出目录，界面只显示文件路径与大小。</p>
      <el-button :loading="exporting" @click="doExport">导出报告</el-button>
      <el-alert
        v-if="exportResult"
        type="success"
        :closable="false"
        show-icon
        style="margin-top: 12px"
        :title="`已写入 ${formatBytes(exportResult.bytes)}`"
        :description="exportResult.path"
      />
    </section>
  </div>
</template>

<script setup>
import { computed, ref } from 'vue'
import * as api from '../api'

const props = defineProps({
  health: { type: Object, default: null },
  talker: { type: String, default: '' },
  overview: { type: Object, default: null },
})

const emit = defineEmits(['refresh'])

const modelVersion = ref('')
const starting = ref(false)
const canceling = ref(false)
const exporting = ref(false)
const message = ref(null)
const exportResult = ref(null)

// The health block names the newest active job in the whole database; that is
// the one a progress bar can attach to, since overview.analysis carries
// done/total but no id.
const activeJob = computed(() => (props.health && props.health.job) || null)
const isActive = computed(
  () => Boolean(activeJob.value) && ['queued', 'running'].includes(activeJob.value.status),
)

const versions = computed(() => {
  const nlp = props.health && props.health.nlp
  return (nlp && nlp.available_versions) || []
})

const percent = computed(() => {
  const job = activeJob.value
  if (!job || !job.total) return 0
  return Math.max(0, Math.min(100, Math.round((job.done / job.total) * 100)))
})

const STATUS = {
  queued: '排队中',
  running: '运行中',
  done: '已完成',
  failed: '失败',
  canceled: '已取消',
}

function statusLabel(status) {
  return STATUS[status] || status
}

function formatBytes(bytes) {
  const n = Number(bytes) || 0
  if (n >= 1024 * 1024) return `${(n / 1024 / 1024).toFixed(2)} MB`
  if (n >= 1024) return `${(n / 1024).toFixed(1)} KB`
  return `${n} 字节`
}

function payload() {
  const body = {}
  if (props.talker) body.talker = props.talker
  if (modelVersion.value) body.model_version = modelVersion.value
  return body
}

async function start() {
  starting.value = true
  message.value = null
  try {
    const result = await api.createJob(payload())
    message.value = { type: 'success', title: '已开始分析', detail: `任务 #${result.id}` }
    emit('refresh')
  } catch (error) {
    if (error.code === 'job_active' && error.body && error.body.id) {
      message.value = {
        type: 'warning',
        title: '已有进行中的任务',
        detail: `任务 #${error.body.id} 正在运行，可先取消它或等待完成。`,
      }
      emit('refresh')
    } else {
      message.value = { type: 'error', title: error.message, detail: error.hint || '' }
    }
  } finally {
    starting.value = false
  }
}

async function cancel() {
  if (!activeJob.value) return
  canceling.value = true
  try {
    await api.cancelJob(activeJob.value.id)
    message.value = { type: 'success', title: '已请求取消', detail: `任务 #${activeJob.value.id}` }
    emit('refresh')
  } catch (error) {
    message.value = { type: 'error', title: error.message, detail: error.hint || '' }
  } finally {
    canceling.value = false
  }
}

async function doExport() {
  exporting.value = true
  exportResult.value = null
  try {
    exportResult.value = await api.exportReport(payload())
  } catch (error) {
    message.value = { type: 'error', title: error.message, detail: error.hint || '' }
  } finally {
    exporting.value = false
  }
}
</script>
