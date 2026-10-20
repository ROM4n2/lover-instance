<template>
  <div v-if="!overview" class="loading">
    <el-skeleton :rows="8" animated />
  </div>
  <div v-else class="view">
    <section class="cards">
      <div v-for="card in cards" :key="card.label" class="card">
        <div class="k">{{ card.label }}</div>
        <div class="v">{{ card.value }}</div>
        <div v-if="card.sub" class="s">{{ card.sub }}</div>
      </div>
    </section>

    <section class="panel">
      <h2>消息类型分布</h2>
      <p class="note">按微信消息类型计数，灰色为系统/非聊天类消息。</p>
      <EChart v-if="hasTypes" :option="typeOption" height="300px" />
      <el-empty v-else description="暂无消息类型数据" />
    </section>

    <section class="panel">
      <h2>一周活跃热力图</h2>
      <p class="note">横轴小时，纵轴星期；颜色越深消息越多。</p>
      <EChart :option="heatOption" height="340px" />
    </section>

    <section class="panel">
      <h2>高频词</h2>
      <div class="duo">
        <div>
          <h3>我 · 高频词</h3>
          <EChart v-if="hasSelfWords" :option="selfCloud" height="300px" />
          <el-empty v-else description="暂无分词结果" />
        </div>
        <div>
          <h3>对方 · 高频词</h3>
          <EChart v-if="hasPartnerWords" :option="partnerCloud" height="300px" />
          <el-empty v-else description="暂无分词结果" />
        </div>
      </div>
    </section>

    <section class="panel">
      <h2>表情使用</h2>
      <div class="duo">
        <div>
          <h3>我</h3>
          <EChart v-if="hasSelfEmoji" :option="selfEmojiOption" height="280px" />
          <el-empty v-else description="暂无表情数据" />
        </div>
        <div>
          <h3>对方</h3>
          <EChart v-if="hasPartnerEmoji" :option="partnerEmojiOption" height="280px" />
          <el-empty v-else description="暂无表情数据" />
        </div>
      </div>
    </section>
  </div>
</template>

<script setup>
import { computed } from 'vue'
import EChart from '../components/EChart.vue'

const props = defineProps({
  overview: { type: Object, default: null },
})

// The API serializes the Go report.View verbatim, so the keys are the Go field
// names — capitalized (View.Meta.TalkerHash, View.Summary.SelfMsgs, ...). The
// view object is reached with a default empty shape so every computed below can
// read a field without a null check on each line.
const view = computed(() => {
  const base = props.overview && props.overview.view
  return base || {}
})

const summary = computed(() => view.value.Summary || {})
const reply = computed(() => summary.value.Reply || {})

const SELF = '#2b6cb0'
const PARTNER = '#c53030'
const PALETTE = ['#3182ce', '#38a169', '#805ad5', '#c53030', '#d69e2e', '#dd6b20', '#319795', '#718096']
const WEEKDAYS = ['周一', '周二', '周三', '周四', '周五', '周六', '周日']

function pct(value) {
  return `${(Number(value || 0) * 100).toFixed(1)}%`
}

function ms(value) {
  const n = Number(value || 0)
  return n >= 1000 ? `${(n / 1000).toFixed(1)} 秒` : `${n} 毫秒`
}

const cards = computed(() => {
  const s = summary.value
  const r = reply.value
  return [
    { label: '我方消息', value: s.SelfMsgs ?? 0, sub: `${s.AvgSelfLen ? s.AvgSelfLen.toFixed(1) : 0} 字/条` },
    { label: '对方消息', value: s.PartnerMsgs ?? 0, sub: `${s.AvgPartnerLen ? s.AvgPartnerLen.toFixed(1) : 0} 字/条` },
    { label: '会话天数', value: s.DayCount ?? 0 },
    { label: '会话段数', value: s.ConversationCount ?? 0 },
    { label: '深夜消息占比', value: pct(s.LateNightRatio) },
    { label: '主动开启占比', value: pct(s.InitiatorRatio) },
    { label: '回复中位数', value: ms(r.MedianMs), sub: `P90 ${ms(r.P90Ms)}` },
    { label: '快速回复占比', value: pct(r.FastRatio), sub: `${r.SampleCount ?? 0} 个样本` },
  ]
})

// --- message types -------------------------------------------------------

const typeLabels = {
  1: '文本',
  3: '图片',
  34: '语音',
  37: '好友请求',
  42: '名片',
  43: '视频',
  47: '表情',
  48: '位置',
  49: '分享/文件',
  50: '通话',
  10000: '系统',
  10002: '撤回',
}

const types = computed(() => summary.value.Types || [])
const hasTypes = computed(() => types.value.length > 0)

const typeOption = computed(() => {
  const pairs = [...types.value].sort((a, b) => (b.Count || 0) - (a.Count || 0))
  return {
    grid: { left: 96, right: 32, top: 8, bottom: 24 },
    tooltip: { trigger: 'axis', axisPointer: { type: 'shadow' } },
    xAxis: { type: 'value' },
    yAxis: {
      type: 'category',
      data: pairs.map((p) => typeLabels[p.MsgType] || `类型 ${p.MsgType}`),
    },
    series: [
      {
        type: 'bar',
        data: pairs.map((p, i) => ({
          value: p.Count || 0,
          itemStyle: { color: (p.MsgType >= 10000) ? '#cbd5e0' : PALETTE[i % PALETTE.length] },
        })),
        barMaxWidth: 16,
        label: { show: true, position: 'right' },
      },
    ],
  }
})

// --- heatmap -------------------------------------------------------------

const heatOption = computed(() => {
  const cells = view.value.Heatmap || []
  let max = 0
  const points = cells.map((cell) => {
    const total = (cell.SelfCount || 0) + (cell.PartnerCount || 0)
    if (total > max) max = total
    return { value: [cell.Hour, cell.Weekday, total], self: cell.SelfCount, partner: cell.PartnerCount }
  })
  return {
    grid: { left: 46, right: 16, top: 12, bottom: 40 },
    tooltip: {
      formatter: (p) =>
        `${WEEKDAYS[p.data.value[1]] || ''} ${p.data.value[0]} 点<br/>我 ${p.data.self} 条 · 对方 ${p.data.partner} 条`,
    },
    xAxis: {
      type: 'category',
      data: Array.from({ length: 24 }, (_, hour) => hour),
      splitArea: { show: true },
    },
    yAxis: { type: 'category', data: WEEKDAYS, splitArea: { show: true } },
    visualMap: {
      min: 0,
      max: max || 1,
      calculable: true,
      orient: 'horizontal',
      left: 'center',
      bottom: 0,
      inRange: { color: ['#ebf4fb', '#9ec6e6', '#4a86b8', '#1d4e79'] },
    },
    series: [{ type: 'heatmap', data: points, emphasis: { itemStyle: { borderColor: '#1a202c', borderWidth: 1 } } }],
  }
})

// --- word clouds ---------------------------------------------------------

const words = computed(() => view.value.Words || {})
const selfWords = computed(() => words.value.Self || [])
const partnerWords = computed(() => words.value.Partner || [])
const hasSelfWords = computed(() => selfWords.value.length > 0)
const hasPartnerWords = computed(() => partnerWords.value.length > 0)

function wordCloudOption(list, color) {
  return {
    tooltip: { formatter: (p) => `${p.name} · ${p.value} 次` },
    series: [
      {
        type: 'wordCloud',
        shape: 'circle',
        width: '96%',
        height: '96%',
        sizeRange: [12, 46],
        rotationRange: [-45, 45],
        rotationStep: 15,
        gridSize: 6,
        drawOutOfBound: false,
        textStyle: { color: () => color },
        data: list.map((entry) => ({ name: entry.Word, value: entry.Count })),
      },
    ],
  }
}

const selfCloud = computed(() => wordCloudOption(selfWords.value, SELF))
const partnerCloud = computed(() => wordCloudOption(partnerWords.value, PARTNER))

// --- emoji ---------------------------------------------------------------

const emoji = computed(() => view.value.Emoji || {})
const selfEmoji = computed(() => emoji.value.Self || [])
const partnerEmoji = computed(() => emoji.value.Partner || [])
const hasSelfEmoji = computed(() => selfEmoji.value.length > 0)
const hasPartnerEmoji = computed(() => partnerEmoji.value.length > 0)

function emojiOption(list, color) {
  const pairs = [...list].sort((a, b) => (b.Count || 0) - (a.Count || 0)).slice(0, 12)
  return {
    grid: { left: 72, right: 28, top: 8, bottom: 20 },
    tooltip: { trigger: 'axis', axisPointer: { type: 'shadow' } },
    xAxis: { type: 'value' },
    yAxis: { type: 'category', data: pairs.map((p) => p.Word) },
    series: [
      {
        type: 'bar',
        data: pairs.map((p) => p.Count || 0),
        itemStyle: { color },
        barMaxWidth: 14,
        label: { show: true, position: 'right' },
      },
    ],
  }
}

const selfEmojiOption = computed(() => emojiOption(selfEmoji.value, SELF))
const partnerEmojiOption = computed(() => emojiOption(partnerEmoji.value, PARTNER))
</script>
