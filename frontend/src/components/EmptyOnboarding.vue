<template>
  <div class="onboarding-card">
    <div class="icon">✨</div>
    <h2>开启你的聊天关系分析</h2>
    <p class="intro">
      数据库当前为空，你可以载入内置演示数据快速体验，或按向导导入微信聊天记录。
    </p>

    <!-- 3步保姆级取数指引 -->
    <div class="steps-guide">
      <div class="step-item">
        <div class="step-num">1</div>
        <div class="step-text">
          <div class="step-title">获取离线导出工具</div>
          <div class="step-desc">使用开源工具 chatlog-keeper 本地提取</div>
        </div>
      </div>
      <div class="step-arrow">→</div>
      <div class="step-item">
        <div class="step-num">2</div>
        <div class="step-text">
          <div class="step-title">电脑端导出 JSON</div>
          <div class="step-desc">登录微信后点击工具中的「导出 JSON」</div>
        </div>
      </div>
      <div class="step-arrow">→</div>
      <div class="step-item">
        <div class="step-num">3</div>
        <div class="step-text">
          <div class="step-title">一键载入分析</div>
          <div class="step-desc">点击「扫描最新导出」或拖拽文件入库</div>
        </div>
      </div>
    </div>

    <!-- 扫描发现的导出文件提示 -->
    <div v-if="scannedExport" class="scan-alert">
      <div class="scan-info">
        <span class="scan-tag">发现最新导出</span>
        <strong>{{ scannedExport.filename }}</strong>
        <span class="scan-size">({{ formatSize(scannedExport.size) }})</span>
      </div>
      <el-button type="success" size="small" :loading="importingScanned" @click="handleImportScanned">
        一键载入该文件
      </el-button>
    </div>

    <div class="actions">
      <div class="action-box">
        <h3>零门槛快速体验</h3>
        <p>载入内置 995 条合成情侣语料，体验图表、情感走势与高光里程碑全貌。</p>
        <el-button type="primary" :loading="demoLoading" @click="handleDemo">
          体验演示数据 (Load Demo)
        </el-button>
      </div>

      <div class="action-box">
        <div class="box-header">
          <h3>导入聊天记录文件</h3>
          <el-button link type="primary" size="small" :loading="scanning" @click="handleScan">
            🔍 自动扫描下载目录
          </el-button>
        </div>
        <p>支持外部导出的 chatlog-keeper JSON 文件（单文件上限 100MB）。</p>
        <div
          :class="['dropzone', { active: isDragging, uploading }]"
          @dragover.prevent="isDragging = true"
          @dragleave.prevent="isDragging = false"
          @drop.prevent="handleDrop"
          @click="triggerFileInput"
        >
          <input
            ref="fileInput"
            type="file"
            accept=".json"
            style="display: none"
            @change="handleFileChange"
          />
          <div v-if="uploading">
            <el-icon class="is-loading"><Loading /></el-icon>
            <div>正在解析并入库...</div>
          </div>
          <div v-else>
            <div>点击或拖拽聊天记录 JSON 到此处</div>
          </div>
        </div>
      </div>
    </div>
  </div>
</template>

<script setup>
import { onMounted, ref } from 'vue'
import { Loading } from '@element-plus/icons-vue'
import { ElMessage } from 'element-plus'
import * as api from '../api'

const emit = defineEmits(['imported', 'demo'])

const demoLoading = ref(false)
const uploading = ref(false)
const scanning = ref(false)
const importingScanned = ref(false)
const isDragging = ref(false)
const fileInput = ref(null)
const scannedExport = ref(null)

function formatSize(bytes) {
  if (!bytes) return '0 B'
  const k = 1024
  const sizes = ['B', 'KB', 'MB', 'GB']
  const i = Math.floor(Math.log(bytes) / Math.log(k))
  return (bytes / Math.pow(k, i)).toFixed(1) + ' ' + sizes[i]
}

async function handleScan() {
  scanning.value = true
  try {
    const res = await api.scanExport(false)
    if (res.found) {
      scannedExport.value = res
      ElMessage.success(`发现导出文件：${res.filename}`)
    } else {
      scannedExport.value = null
      ElMessage.info(res.message || '未在下载目录或常用导出路径发现有效聊天记录 JSON')
    }
  } catch (err) {
    ElMessage.error(err.message || '扫描失败')
  } finally {
    scanning.value = false
  }
}

async function handleImportScanned() {
  importingScanned.value = true
  try {
    const res = await api.scanExport(true)
    if (res.ok) {
      ElMessage.success(`载入成功！共解析 ${res.parsed || 0} 条消息`)
      emit('imported')
    }
  } catch (err) {
    ElMessage.error(err.message || '导入失败')
  } finally {
    importingScanned.value = false
  }
}

async function handleDemo() {
  demoLoading.value = true
  try {
    await api.loadDemo()
    ElMessage.success('演示数据载入成功！')
    emit('imported')
  } catch (err) {
    ElMessage.error(err.message || '载入演示数据失败')
  } finally {
    demoLoading.value = false
  }
}

function triggerFileInput() {
  if (fileInput.value) fileInput.value.click()
}

function handleFileChange(e) {
  const files = e.target.files
  if (files && files[0]) {
    uploadFile(files[0])
  }
}

function handleDrop(e) {
  isDragging.value = false
  const files = e.dataTransfer.files
  if (files && files[0]) {
    uploadFile(files[0])
  }
}

async function uploadFile(file) {
  if (!file.name.toLowerCase().endsWith('.json')) {
    ElMessage.warning('仅支持 .json 格式聊天文件')
    return
  }
  uploading.value = true
  try {
    const res = await api.uploadExport(file)
    ElMessage.success(`导入成功！共解析 ${res.parsed || 0} 条消息`)
    emit('imported')
  } catch (err) {
    ElMessage.error(err.message || '上传导入失败')
  } finally {
    uploading.value = false
  }
}

onMounted(() => {
  api.scanExport(false).then(res => {
    if (res && res.found) scannedExport.value = res
  }).catch(() => {})
})
</script>

<style scoped>
.onboarding-card {
  max-width: 840px;
  margin: 28px auto;
  padding: 32px 28px;
  background: #ffffff;
  border: 1px solid #e2e8f0;
  border-radius: 12px;
  text-align: center;
}
.icon {
  font-size: 36px;
  margin-bottom: 8px;
}
h2 {
  font-size: 20px;
  margin-bottom: 8px;
  color: #1e293b;
}
.intro {
  color: #64748b;
  font-size: 14px;
  max-width: 600px;
  margin: 0 auto 24px;
  line-height: 1.6;
}

/* 3步指引 */
.steps-guide {
  display: flex;
  align-items: center;
  justify-content: space-between;
  background: #f8fafc;
  border: 1px solid #e2e8f0;
  border-radius: 8px;
  padding: 16px 20px;
  margin-bottom: 24px;
  text-align: left;
}
@media (max-width: 680px) {
  .steps-guide {
    flex-direction: column;
    gap: 12px;
  }
  .step-arrow {
    display: none;
  }
}
.step-item {
  display: flex;
  align-items: center;
  gap: 12px;
}
.step-num {
  width: 26px;
  height: 26px;
  border-radius: 50%;
  background: #2b6cb0;
  color: #ffffff;
  font-size: 13px;
  font-weight: 600;
  display: flex;
  align-items: center;
  justify-content: center;
  flex-shrink: 0;
}
.step-title {
  font-size: 13px;
  font-weight: 600;
  color: #1e293b;
}
.step-desc {
  font-size: 11px;
  color: #64748b;
  margin-top: 2px;
}
.step-arrow {
  color: #94a3b8;
  font-weight: bold;
}

/* 扫描发现提示 */
.scan-alert {
  display: flex;
  align-items: center;
  justify-content: space-between;
  background: #f0fdf4;
  border: 1px solid #bbf7d0;
  padding: 12px 18px;
  border-radius: 8px;
  margin-bottom: 20px;
  text-align: left;
}
.scan-tag {
  background: #22c55e;
  color: #fff;
  font-size: 11px;
  padding: 2px 6px;
  border-radius: 4px;
  margin-right: 8px;
}
.scan-size {
  color: #64748b;
  font-size: 12px;
  margin-left: 6px;
}

.actions {
  display: grid;
  grid-template-columns: 1fr 1fr;
  gap: 20px;
  text-align: left;
}
@media (max-width: 640px) {
  .actions {
    grid-template-columns: 1fr;
  }
}
.action-box {
  border: 1px solid #f1f5f9;
  background: #f8fafc;
  padding: 20px;
  border-radius: 8px;
  display: flex;
  flex-direction: column;
}
.action-box h3 {
  font-size: 16px;
  margin-bottom: 6px;
  color: #334155;
}
.action-box p {
  font-size: 13px;
  color: #64748b;
  margin-bottom: 16px;
  flex: 1;
  line-height: 1.5;
}
.dropzone {
  border: 2px dashed #cbd5e1;
  border-radius: 8px;
  padding: 24px 12px;
  text-align: center;
  color: #64748b;
  font-size: 13px;
  cursor: pointer;
  transition: all 0.2s;
  background: #ffffff;
}
.dropzone.active {
  border-color: #2b6cb0;
  background: #eff6ff;
}
</style>
