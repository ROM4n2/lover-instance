<template>
  <div v-if="!overview" class="loading">
    <el-skeleton :rows="8" animated />
  </div>
  <div v-else class="view">
    <!-- The gate. While a scoring run is in progress, no sentiment chart is
         drawn from the partial data: a half-scored conversation drawn as a
         complete curve is the specific misleading output this whole milestone
         forbids. The reader gets absolute progress instead. -->
    <section v-if="running" class="panel">
      <h2>分析中</h2>
      <p class="note">
        情感打分尚未完成，曲线与对比会在完成后自动出现。以下进度是已打分的绝对条数。
      </p>
      <el-progress :percentage="percent" :stroke-width="18" :text-inside="true" />
      <p class="counter">{{ analysis.done }} / {{ analysis.total }} 条已打分</p>
    </section>

    <template v-else>
      <section class="panel">
        <h2>情绪构成</h2>
        <p class="note">
          七类情绪来自 {{ view.EmotionSource || '（无词典版本）' }}（词典规则匹配），
          仅覆盖成功打分的消息；未匹配的词记为「中性」，不是「平静」。
        </p>
        <div class="duo">
          <div>
            <h3>七类占比（雷达）</h3>
            <EChart v-if="hasRadar" :option="radarOption" height="340px" />
            <el-empty v-else description="本次没有词典情绪数据" />
          </div>
          <div>
            <h3>各类计数</h3>
            <EChart v-if="hasEmotionCounts" :option="emotionBarOption" height="340px" />
            <el-empty v-else description="本次没有词典情绪数据" />
          </div>
        </div>
        <p class="counter">正向占比（中性除外）：{{ pct(emotion.PositiveRatio) }}</p>
      </section>

      <section class="panel">
        <h2>情感趋势 · 7 天滚动均值</h2>
        <p class="note">
          样本不足（窗口内 &lt; 3 条）的日子不画均值；浅色带是 ±1 SD（消息离散度，不是置信区间）。
        </p>
        <EChart v-if="hasTrend" :option="trendOption" height="380px" />
        <el-empty v-else description="暂无可绘制的趋势点" />
      </section>

      <section v-if="comparison.Present" class="panel">
        <h2>模型版本对比</h2>
        <p class="note">
          同一个会话被两个版本各打了一次分。差异率＝分歧条数／两版都打分的条数。
        </p>
        <div class="duo">
          <div>
            <h3>{{ comparison.Current.Version || '当前版本' }}</h3>
            <p>正向 {{ comparison.Current.Positive }} · 负向 {{ comparison.Current.Negative }} · 中性 {{ comparison.Current.Neutral }}</p>
            <p class="counter">正向占比 {{ pct(comparison.Current.PositiveRatio) }}</p>
          </div>
          <div>
            <h3>{{ comparison.Other.Version || '对比版本' }}</h3>
            <p>正向 {{ comparison.Other.Positive }} · 负向 {{ comparison.Other.Negative }} · 中性 {{ comparison.Other.Neutral }}</p>
            <p class="counter">正向占比 {{ pct(comparison.Other.PositiveRatio) }}</p>
          </div>
        </div>
        <p class="counter">
          两版都打分 {{ comparison.BothScored }} 条，分歧 {{ comparison.Differing }} 条，差异率 {{ pct(comparison.DisagreementRate) }}
        </p>
      </section>
    </template>

    <section class="panel">
      <h2>打分覆盖率</h2>
      <p class="note">已打分 {{ coverage.Scored }} / {{ coverage.Total }} 条消息。</p>
      <el-progress :percentage="coveragePercent" />
      <p v-if="coverage.Partial" class="counter">覆盖率不足 100%，尚未打分的消息不参与上述统计。</p>
    </section>
  </div>
</template>

<script setup>
import { computed } from 'vue'
import EChart from '../components/EChart.vue'

const props = defineProps({
  overview: { type: Object, default: null },
  analysis: { type: Object, default: () => ({ running: false, done: 0, total: 0 }) },
})

const SELF = '#2b6cb0'
const EMO_LABELS = ['好', '乐', '哀', '怒', '惧', '恶', '惊']
const PALETTE = ['#3182ce', '#38a169', '#805ad5', '#c53030', '#d69e2e', '#dd6b20', '#319795']

const view = computed(() => (props.overview && props.overview.view) || {})

// The gate flag. It is read from the overview's analysis block, which the
// server derives from the same single-flight slot POST /api/jobs writes to.
const running = computed(() => Boolean(props.analysis && props.analysis.running))

const percent = computed(() => {
  const total = Number(props.analysis && props.analysis.total) || 0
  const done = Number(props.analysis && props.analysis.done) || 0
  if (total <= 0) return 0
  return Math.max(0, Math.min(100, Math.round((done / total) * 100)))
})

function pct(value) {
  return `${(Number(value || 0) * 100).toFixed(1)}%`
}

// --- emotion -------------------------------------------------------------

const emotion = computed(() => view.value.Emotion || {})

const radarValues = computed(() => (Array.isArray(emotion.value.Radar) ? emotion.value.Radar : []))
const hasRadar = computed(() => radarValues.value.length === EMO_LABELS.length)
const hasEmotionCounts = computed(() => (emotion.value.ScoredTotal || 0) > 0)

const radarOption = computed(() => ({
  tooltip: {},
  radar: {
    indicator: EMO_LABELS.map((label) => ({ name: label, max: 1 })),
    radius: '62%',
    splitNumber: 4,
  },
  series: [
    {
      type: 'radar',
      areaStyle: { opacity: 0.25 },
      lineStyle: { color: SELF },
      itemStyle: { color: SELF },
      data: [{ value: radarValues.value.map((v) => Number(Number(v).toFixed(4))), name: '占比' }],
    },
  ],
}))

const emotionBarOption = computed(() => {
  const counts = emotion.value.Counts || {}
  return {
    grid: { left: 48, right: 16, top: 16, bottom: 28 },
    tooltip: { trigger: 'axis', axisPointer: { type: 'shadow' } },
    xAxis: { type: 'category', data: EMO_LABELS, axisLabel: { interval: 0 } },
    yAxis: { type: 'value' },
    series: [
      {
        type: 'bar',
        barMaxWidth: 32,
        data: EMO_LABELS.map((label, i) => ({
          value: counts[label] || 0,
          itemStyle: { color: PALETTE[i % PALETTE.length] },
        })),
      },
    ],
  }
})

// --- trend ---------------------------------------------------------------

const trend = computed(() => view.value.Trend || {})
const trendPoints = computed(() => trend.value.Points || [])
const hasTrend = computed(() => trendPoints.value.some((p) => p.Drawable))

const trendOption = computed(() => {
  const points = trendPoints.value
  const days = points.map((p) => p.Day)
  const mean = points.map((p) => (p.Drawable ? Number(Number(p.Mean).toFixed(4)) : null))
  const bandLow = points.map((p) => (p.Drawable ? Number((p.Mean - p.StdDev).toFixed(4)) : null))
  const bandSpan = points.map((p) => (p.Drawable ? Number((2 * p.StdDev).toFixed(4)) : null))
  const thin = points
    .filter((p) => !p.Drawable)
    .map((p) => [p.Day, Number(Number(p.Mean).toFixed(4))])
  const span = Number(trend.value.Axis) || 0.2

  return {
    grid: { left: 56, right: 16, top: 16, bottom: 28 },
    tooltip: { trigger: 'axis' },
    legend: { data: ['7 天滚动均值', '±1 SD', '样本不足'], bottom: 0 },
    xAxis: { type: 'category', data: days, axisLabel: { hideOverlap: true } },
    yAxis: {
      type: 'value',
      min: -span,
      max: span,
      axisLabel: { formatter: (v) => Number(v).toFixed(2) },
    },
    series: [
      {
        name: '±1 SD',
        type: 'line',
        stack: 'sd',
        data: bandLow,
        connectNulls: false,
        lineStyle: { opacity: 0 },
        symbol: 'none',
        silent: true,
      },
      {
        name: '±1 SD',
        type: 'line',
        stack: 'sd',
        data: bandSpan,
        connectNulls: false,
        lineStyle: { opacity: 0 },
        symbol: 'none',
        silent: true,
        areaStyle: { color: 'rgba(43,108,176,0.18)' },
      },
      {
        name: '7 天滚动均值',
        type: 'line',
        data: mean,
        connectNulls: false,
        symbol: 'circle',
        symbolSize: 4,
        lineStyle: { width: 2, color: SELF },
        itemStyle: { color: SELF },
        markLine: {
          silent: true,
          symbol: 'none',
          label: { show: false },
          lineStyle: { color: '#cbd5e0', type: 'dashed' },
          data: [{ yAxis: 0 }],
        },
      },
      {
        name: '样本不足',
        type: 'scatter',
        data: thin,
        symbolSize: 9,
        itemStyle: { color: '#d69e2e' },
      },
    ],
  }
})

// --- comparison ----------------------------------------------------------

const comparison = computed(() => view.value.Comparison || {})

// --- coverage ------------------------------------------------------------

const coverage = computed(() => view.value.Coverage || {})
const coveragePercent = computed(() => {
  const total = Number(coverage.value.Total) || 0
  const scored = Number(coverage.value.Scored) || 0
  if (total <= 0) return 0
  return Math.max(0, Math.min(100, Math.round((scored / total) * 100)))
})
</script>
