<template>
  <div v-if="loading" class="loading">
    <el-skeleton :rows="6" animated />
  </div>
  <div v-else-if="!highlights || !highlights.present" class="panel">
    <el-empty description="暂无高光里程碑数据（请先导入聊天记录）" />
  </div>
  <div v-else class="view">
    <section class="cards">
      <div class="card">
        <div class="k">启程之日</div>
        <div class="v">{{ highlights.start_date }}</div>
        <div class="s">已走过 {{ highlights.journey_days }} 天</div>
      </div>
      <div class="card">
        <div class="k">最长连续聊天</div>
        <div class="v">{{ highlights.max_streak_days }} 天</div>
        <div class="s">{{ highlights.streak_period }}</div>
      </div>
      <div class="card card-action" @click="openShareModal">
        <div class="k">仪式感分享</div>
        <div class="v action-text">📸 生成分享卡片</div>
        <div class="s">制作 3:4 手机纪念海报</div>
      </div>
    </section>

    <!-- 关系成就勋章 -->
    <section v-if="highlights.badges && highlights.badges.length" class="panel">
      <h2>关系成就勋章</h2>
      <p class="note">基于真实聊天互动推导的专属勋章，见证每一次深聊与默契。</p>
      <div class="badges-grid">
        <div v-for="b in highlights.badges" :key="b.id" :class="['badge-card', `badge-${b.level}`]">
          <div class="b-icon">{{ b.icon }}</div>
          <div class="b-text">
            <div class="b-title">{{ b.title }} <span class="b-sub">({{ b.subtitle }})</span></div>
            <div class="b-desc">{{ b.description }}</div>
          </div>
        </div>
      </div>
    </section>

    <section class="panel">
      <h2>关系高光事件轴</h2>
      <p class="note">记录了你们相遇以来的标志性时刻、聊天峰值与深夜畅聊。</p>

      <div class="timeline">
        <div v-for="(ev, idx) in highlights.events" :key="idx" class="timeline-item">
          <div class="t-badge">{{ ev.date }}</div>
          <div class="t-content">
            <div class="t-title">{{ ev.title }}</div>
            <div class="t-desc">{{ ev.description }}</div>
          </div>
        </div>
      </div>
    </section>

    <!-- 分享卡片弹窗 -->
    <el-dialog v-model="showShareModal" title="📸 恋爱纪念卡片 (3:4 海报)" width="440px" align-center @opened="renderCanvas">
      <div class="card-preview-wrap">
        <canvas ref="canvasRef" width="720" height="960" class="share-canvas"></canvas>
      </div>
      <template #footer>
        <div class="dialog-footer">
          <el-button @click="copyCard">复制图片</el-button>
          <el-button type="primary" @click="downloadCard">下载图片 (PNG)</el-button>
        </div>
      </template>
    </el-dialog>
  </div>
</template>

<script setup>
import { nextTick, onMounted, ref, watch } from 'vue'
import { ElMessage } from 'element-plus'
import * as api from '../api'

const props = defineProps({
  talker: { type: String, default: '' },
})

const loading = ref(false)
const highlights = ref(null)
const showShareModal = ref(false)
const canvasRef = ref(null)

async function load() {
  loading.value = true
  try {
    highlights.value = await api.getHighlights(props.talker)
  } catch {
    highlights.value = null
  } finally {
    loading.value = false
  }
}

function openShareModal() {
  showShareModal.value = true
  nextTick(() => {
    renderCanvas()
  })
}

function renderCanvas() {
  const canvas = canvasRef.value
  if (!canvas || !highlights.value) return
  const ctx = canvas.getContext('2d')
  const h = highlights.value

  const grad = ctx.createLinearGradient(0, 0, 720, 960)
  grad.addColorStop(0, '#0f172a')
  grad.addColorStop(0.5, '#1e1b4b')
  grad.addColorStop(1, '#0f172a')
  ctx.fillStyle = grad
  ctx.fillRect(0, 0, 720, 960)

  ctx.strokeStyle = '#334155'
  ctx.lineWidth = 2
  ctx.strokeRect(24, 24, 672, 912)

  ctx.fillStyle = '#94a3b8'
  ctx.font = '20px sans-serif'
  ctx.textAlign = 'center'
  ctx.fillText('L O V E R   I N S T A N C E', 360, 80)

  ctx.fillStyle = '#f8fafc'
  ctx.font = 'bold 36px sans-serif'
  ctx.fillText('我们的聊天恋爱记忆', 360, 130)

  ctx.fillStyle = '#1e293b'
  ctx.fillRect(50, 170, 620, 150)
  ctx.strokeStyle = '#475569'
  ctx.strokeRect(50, 170, 620, 150)

  ctx.fillStyle = '#38bdf8'
  ctx.font = 'bold 48px sans-serif'
  ctx.fillText(`已相伴 ${h.journey_days || 0} 天`, 360, 240)

  ctx.fillStyle = '#94a3b8'
  ctx.font = '20px sans-serif'
  ctx.fillText(`始于 ${h.start_date || '—'} · 最长连续互发 ${h.max_streak_days || 0} 天`, 360, 285)

  ctx.textAlign = 'left'
  let y = 370
  ctx.fillStyle = '#f1f5f9'
  ctx.font = 'bold 24px sans-serif'
  ctx.fillText('✨ 标志性长谈与高光', 60, y)

  y += 40
  ctx.font = '20px sans-serif'
  if (h.events && h.events.length) {
    for (let i = 0; i < Math.min(h.events.length, 4); i++) {
      const ev = h.events[i]
      ctx.fillStyle = '#38bdf8'
      ctx.fillText(`• ${ev.date}`, 70, y)
      ctx.fillStyle = '#e2e8f0'
      ctx.fillText(`${ev.title}`, 220, y)
      y += 36
    }
  }

  y = 600
  ctx.fillStyle = '#f1f5f9'
  ctx.font = 'bold 24px sans-serif'
  ctx.fillText('🏅 关系成就勋章', 60, y)

  y += 45
  if (h.badges && h.badges.length) {
    for (let i = 0; i < Math.min(h.badges.length, 3); i++) {
      const b = h.badges[i]
      ctx.fillStyle = '#1e293b'
      ctx.fillRect(60, y, 600, 56)
      ctx.strokeStyle = b.level === 'gold' ? '#eab308' : '#94a3b8'
      ctx.strokeRect(60, y, 600, 56)

      ctx.fillStyle = '#f8fafc'
      ctx.font = 'bold 22px sans-serif'
      ctx.fillText(`${b.icon} ${b.title}`, 80, y + 36)

      ctx.fillStyle = '#94a3b8'
      ctx.font = '18px sans-serif'
      ctx.fillText(`${b.subtitle}`, 240, y + 36)
      y += 70
    }
  }

  ctx.fillStyle = '#64748b'
  ctx.font = '16px sans-serif'
  ctx.textAlign = 'center'
  ctx.fillText('基于本地私有数据分析 · 零云端上传 · 纯真情实感', 360, 890)
}

function downloadCard() {
  const canvas = canvasRef.value
  if (!canvas) return
  const link = document.createElement('a')
  link.download = `lover-memory-${highlights.value.start_date || 'card'}.png`
  link.href = canvas.toDataURL('image/png')
  link.click()
  ElMessage.success('纪念卡片已保存到下载目录！')
}

async function copyCard() {
  const canvas = canvasRef.value
  if (!canvas) return
  try {
    canvas.toBlob(blob => {
      navigator.clipboard.write([
        new ClipboardItem({ 'image/png': blob })
      ]).then(() => {
        ElMessage.success('已复制到剪贴板，可直接粘贴分享！')
      }).catch(() => {
        ElMessage.warning('剪贴板写入失败，请直接使用下载按钮')
      })
    })
  } catch {
    ElMessage.warning('当前环境不支持直接复制图片，请使用下载按钮')
  }
}

watch(() => props.talker, () => load())
onMounted(() => load())
</script>

<style scoped>
.card-action {
  cursor: pointer;
  border-color: #93c5fd;
  background: #f0f9ff;
  transition: all 0.2s;
}
.card-action:hover {
  border-color: #2b6cb0;
  box-shadow: 0 2px 8px rgba(43, 108, 176, 0.15);
}
.action-text {
  color: #2b6cb0;
  font-size: 16px;
}

/* 勋章网格 */
.badges-grid {
  display: grid;
  grid-template-columns: repeat(auto-fit, minmax(220px, 1fr));
  gap: 12px;
  margin-top: 14px;
}
.badge-card {
  display: flex;
  align-items: center;
  gap: 12px;
  padding: 12px 14px;
  border-radius: 8px;
  border: 1px solid #e2e8f0;
  background: #fafafa;
}
.badge-card.badge-gold {
  border-color: #fef08a;
  background: #fefce8;
}
.badge-card.badge-silver {
  border-color: #e2e8f0;
  background: #f8fafc;
}
.badge-card.badge-bronze {
  border-color: #fed7aa;
  background: #fff7ed;
}
.b-icon {
  font-size: 26px;
}
.b-title {
  font-size: 14px;
  font-weight: 600;
  color: #1e293b;
}
.b-sub {
  font-size: 12px;
  color: #64748b;
  font-weight: normal;
}
.b-desc {
  font-size: 12px;
  color: #64748b;
  margin-top: 2px;
  line-height: 1.4;
}

/* 分享弹窗 */
.card-preview-wrap {
  display: flex;
  justify-content: center;
  margin: 8px 0;
}
.share-canvas {
  width: 100%;
  max-width: 360px;
  border-radius: 8px;
  box-shadow: 0 4px 14px rgba(0, 0, 0, 0.15);
}
.dialog-footer {
  display: flex;
  justify-content: flex-end;
  gap: 10px;
}
</style>
