<template>
  <div class="onboarding-card">
    <div class="icon">✨</div>
    <h2>开启你的聊天关系分析</h2>
    <p class="intro">
      数据库当前为空，你可以载入内置演示数据快速体验，或直接拖拽外部导出的聊天记录文件开始分析。
    </p>

    <div class="actions">
      <div class="action-box">
        <h3>零门槛快速体验</h3>
        <p>载入内置 995 条合成情侣语料，体验图表、情感走势与高光里程碑全貌。</p>
        <el-button type="primary" :loading="demoLoading" @click="handleDemo">
          体验演示数据 (Load Demo)
        </el-button>
      </div>

      <div class="action-box">
        <h3>拖拽导入聊天文件</h3>
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
import { ref } from 'vue'
import { Loading } from '@element-plus/icons-vue'
import { ElMessage } from 'element-plus'
import * as api from '../api'

const emit = defineEmits(['imported', 'demo'])

const demoLoading = ref(false)
const uploading = ref(false)
const isDragging = ref(false)
const fileInput = ref(null)

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
</script>

<style scoped>
.onboarding-card {
  max-width: 800px;
  margin: 32px auto;
  padding: 32px 24px;
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
  max-width: 580px;
  margin: 0 auto 28px;
  line-height: 1.6;
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
