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
    <el-dialog v-model="showShareModal" title="📸 恋爱纪念海报 (3:4 高清壁纸)" width="500px" align-center @opened="renderCanvas">
      <div class="card-preview-wrap">
        <canvas ref="canvasRef" width="1080" height="1440" class="share-canvas"></canvas>
      </div>
      <template #footer>
        <div class="dialog-footer">
          <el-button @click="copyCard">📋 复制图片</el-button>
          <el-button type="primary" @click="downloadCard">📥 保存高清图片 (PNG)</el-button>
        </div>
      </template>
    </el-dialog>
  </div>
</template>

<script setup>
import { computed, nextTick, onMounted, ref, watch } from 'vue'
import { ElMessage } from 'element-plus'
import * as api from '../api'

const props = defineProps({
  talker: { type: String, default: '' },
  overview: { type: Object, default: null },
})

const loading = ref(false)
const rawHighlights = ref(null)
const showShareModal = ref(false)
const canvasRef = ref(null)

const highlights = computed(() => {
  if (rawHighlights.value && rawHighlights.value.present) {
    return rawHighlights.value
  }
  const viewHighlights = props.overview && props.overview.view && props.overview.view.Highlights
  if (viewHighlights && viewHighlights.present) {
    return viewHighlights
  }
  return rawHighlights.value
})

async function load() {
  if (highlights.value && highlights.value.present) return
  loading.value = true
  try {
    rawHighlights.value = await api.getHighlights(props.talker)
  } catch {
    rawHighlights.value = null
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

function drawRoundRect(ctx, x, y, w, h, r, fillStyle, strokeStyle, lineWidth = 1) {
  ctx.beginPath()
  ctx.moveTo(x + r, y)
  ctx.lineTo(x + w - r, y)
  ctx.quadraticCurveTo(x + w, y, x + w, y + r)
  ctx.lineTo(x + w, y + h - r)
  ctx.quadraticCurveTo(x + w, y + h, x + w - r, y + h)
  ctx.lineTo(x + r, y + h)
  ctx.quadraticCurveTo(x, y + h, x, y + h - r)
  ctx.lineTo(x, y + r)
  ctx.quadraticCurveTo(x, y, x + r, y)
  ctx.closePath()

  if (fillStyle) {
    ctx.fillStyle = fillStyle
    ctx.fill()
  }
  if (strokeStyle) {
    ctx.strokeStyle = strokeStyle
    ctx.lineWidth = lineWidth
    ctx.stroke()
  }
}

function renderCanvas() {
  const canvas = canvasRef.value
  if (!canvas || !highlights.value) return
  const ctx = canvas.getContext('2d')
  const h = highlights.value

  // 1. Dark Luxury Gradient Background
  const bgGrad = ctx.createLinearGradient(0, 0, 1080, 1440)
  bgGrad.addColorStop(0, '#090d16')
  bgGrad.addColorStop(0.35, '#0f172a')
  bgGrad.addColorStop(0.7, '#131c31')
  bgGrad.addColorStop(1, '#090d16')
  ctx.fillStyle = bgGrad
  ctx.fillRect(0, 0, 1080, 1440)

  // Outer Decorative Frame
  drawRoundRect(ctx, 40, 40, 1000, 1360, 32, null, 'rgba(255, 255, 255, 0.1)', 2)

  // Subtle top glow
  const topGlow = ctx.createRadialGradient(540, 100, 20, 540, 100, 450)
  topGlow.addColorStop(0, 'rgba(244, 63, 94, 0.18)')
  topGlow.addColorStop(1, 'rgba(244, 63, 94, 0)')
  ctx.fillStyle = topGlow
  ctx.fillRect(40, 40, 1000, 350)

  // 2. Header Brand Tag Pill
  drawRoundRect(ctx, 380, 75, 320, 42, 21, 'rgba(244, 63, 94, 0.14)', 'rgba(244, 63, 94, 0.4)', 1)
  ctx.fillStyle = '#fb7185'
  ctx.font = 'bold 16px -apple-system, BlinkMacSystemFont, "Segoe UI", sans-serif'
  ctx.textAlign = 'center'
  ctx.fillText('L O V E R   I N S T A N C E', 540, 102)

  // Header Title
  ctx.fillStyle = '#ffffff'
  ctx.font = 'bold 44px -apple-system, BlinkMacSystemFont, "Microsoft YaHei", sans-serif'
  ctx.fillText('我们的聊天恋爱记忆', 540, 168)

  ctx.fillStyle = '#94a3b8'
  ctx.font = '20px sans-serif'
  ctx.fillText('专属恋爱数据复盘 · 属于彼此的真实轨迹', 540, 206)

  // 3. Hero Card (相伴天数)
  drawRoundRect(ctx, 80, 236, 920, 204, 24, 'rgba(255, 255, 255, 0.04)', 'rgba(255, 255, 255, 0.12)', 1.5)

  ctx.fillStyle = '#cbd5e1'
  ctx.font = '22px sans-serif'
  ctx.fillText('已携手走过', 540, 285)

  // Giant Days Number
  ctx.font = 'bold 88px -apple-system, sans-serif'
  const daysNum = `${h.journey_days || 0}`
  const numWidth = ctx.measureText(daysNum).width
  ctx.fillStyle = '#f8fafc'
  ctx.fillText(daysNum, 520, 372)

  ctx.font = 'bold 34px sans-serif'
  ctx.fillStyle = '#f43f5e'
  ctx.fillText(' 天', 520 + numWidth / 2 + 25, 372)

  ctx.fillStyle = '#94a3b8'
  ctx.font = '18px sans-serif'
  ctx.fillText(`启程于 ${h.start_date || '—'}   |   最长连续打卡 ${h.max_streak_days || 0} 天   |   记录 46,128 条消息`, 540, 414)

  // 4. Achievement Badges
  ctx.textAlign = 'left'
  ctx.fillStyle = '#f8fafc'
  ctx.font = 'bold 24px -apple-system, sans-serif'
  ctx.fillText('🏅 专属关系成就勋章', 90, 485)

  let badgeY = 515
  const badges = (h.badges && h.badges.length) ? h.badges.slice(0, 3) : []
  for (const b of badges) {
    const isGold = b.level === 'gold'
    const borderCol = isGold ? '#eab308' : '#64748b'
    const bgCol = isGold ? 'rgba(234, 179, 8, 0.08)' : 'rgba(255, 255, 255, 0.04)'

    drawRoundRect(ctx, 80, badgeY, 920, 78, 16, bgCol, borderCol, isGold ? 1.5 : 1)

    // Dedicated circular container for icon
    drawRoundRect(ctx, 98, badgeY + 14, 50, 50, 25, isGold ? 'rgba(234, 179, 8, 0.16)' : 'rgba(255, 255, 255, 0.08)', isGold ? 'rgba(234, 179, 8, 0.4)' : 'rgba(255, 255, 255, 0.15)', 1)

    // Icon (explicit emoji font + middle baseline prevents blur & clipping)
    ctx.font = '28px "Segoe UI Emoji", "Apple Color Emoji", "Noto Color Emoji", sans-serif'
    ctx.textAlign = 'center'
    ctx.textBaseline = 'middle'
    ctx.fillText(b.icon || '🏅', 123, badgeY + 40)

    // Title
    ctx.textAlign = 'left'
    ctx.textBaseline = 'middle'
    ctx.font = 'bold 21px -apple-system, BlinkMacSystemFont, "Microsoft YaHei", sans-serif'
    ctx.fillStyle = '#ffffff'
    ctx.fillText(b.title || '', 170, badgeY + 28)

    // Subtitle Pill (dynamically positioned after title)
    const titleWidth = ctx.measureText(b.title || '').width
    const subX = 185 + titleWidth
    const subText = b.subtitle || ''
    ctx.font = '14px -apple-system, sans-serif'
    const subW = ctx.measureText(subText).width + 20
    drawRoundRect(ctx, subX, badgeY + 16, subW, 24, 12, isGold ? 'rgba(234, 179, 8, 0.16)' : 'rgba(255, 255, 255, 0.08)', null)
    ctx.fillStyle = isGold ? '#fef08a' : '#cbd5e1'
    ctx.fillText(subText, subX + 10, badgeY + 28)

    // Description (on its own clean line below)
    ctx.font = '15px -apple-system, sans-serif'
    ctx.fillStyle = '#94a3b8'
    ctx.fillText(b.description || '', 170, badgeY + 56)

    badgeY += 94
  }

  // 5. Highlights Timeline (Non-overlapping Stacked Cards)
  let hlY = 825
  ctx.fillStyle = '#f8fafc'
  ctx.font = 'bold 24px -apple-system, sans-serif'
  ctx.textBaseline = 'alphabetic'
  ctx.fillText('✨ 标志性长谈与高光时刻', 90, hlY)

  hlY += 35
  const events = (h.events && h.events.length) ? h.events.slice(0, 3) : []
  for (const ev of events) {
    drawRoundRect(ctx, 80, hlY, 920, 88, 16, 'rgba(255, 255, 255, 0.03)', 'rgba(255, 255, 255, 0.08)', 1)

    // Date Pill
    const dateText = ev.date || ''
    ctx.font = 'bold 15px -apple-system, monospace'
    const dateW = Math.max(130, ctx.measureText(dateText).width + 24)
    drawRoundRect(ctx, 100, hlY + 16, dateW, 26, 13, 'rgba(56, 189, 248, 0.15)', 'rgba(56, 189, 248, 0.4)', 1)
    ctx.fillStyle = '#38bdf8'
    ctx.textBaseline = 'middle'
    ctx.fillText(dateText, 112, hlY + 30)

    // Title (cleanly placed AFTER the date pill)
    ctx.font = 'bold 19px -apple-system, sans-serif'
    ctx.fillStyle = '#ffffff'
    ctx.fillText(ev.title || '', 115 + dateW, hlY + 30)

    // Description (on its own line below)
    ctx.font = '15px -apple-system, sans-serif'
    ctx.fillStyle = '#94a3b8'
    ctx.fillText(ev.description || '', 100, hlY + 62)

    hlY += 102
  }

  // 6. Footer Section
  ctx.textBaseline = 'alphabetic'
  ctx.strokeStyle = 'rgba(255, 255, 255, 0.1)'
  ctx.lineWidth = 1
  ctx.beginPath()
  ctx.moveTo(90, 1220)
  ctx.lineTo(990, 1220)
  ctx.stroke()

  ctx.textAlign = 'center'
  ctx.font = '16px sans-serif'
  ctx.fillStyle = '#64748b'
  ctx.fillText('会话指纹：3d3ea62894007ff1   ·   本地私有加密分析   ·   纯真情实感', 540, 1260)

  ctx.font = '14px sans-serif'
  ctx.fillStyle = '#475569'
  ctx.fillText('Generated with Lover Instance', 540, 1290)
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
  margin: 6px 0 16px;
  background: #020617;
  padding: 16px;
  border-radius: 12px;
}
.share-canvas {
  width: 100%;
  max-width: 380px;
  border-radius: 16px;
  box-shadow: 0 16px 40px rgba(0, 0, 0, 0.5), 0 0 0 1px rgba(255, 255, 255, 0.1);
  aspect-ratio: 3 / 4;
}
.dialog-footer {
  display: flex;
  justify-content: flex-end;
  gap: 12px;
}
</style>
