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
  </div>
</template>

<script setup>
import { onMounted, ref, watch } from 'vue'
import * as api from '../api'

const props = defineProps({
  talker: { type: String, default: '' },
})

const loading = ref(false)
const highlights = ref(null)

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

watch(() => props.talker, () => load())
onMounted(() => load())
</script>

<style scoped>
.timeline {
  margin-top: 16px;
  display: flex;
  flex-direction: column;
  gap: 16px;
}
.timeline-item {
  display: flex;
  gap: 16px;
  border-left: 2px solid var(--el-color-primary, #2b6cb0);
  padding-left: 16px;
}
.t-badge {
  font-size: 13px;
  font-weight: 600;
  color: #64748b;
  min-width: 100px;
}
.t-content {
  flex: 1;
}
.t-title {
  font-size: 15px;
  font-weight: 600;
  color: #1e293b;
  margin-bottom: 4px;
}
.t-desc {
  font-size: 13px;
  color: #475569;
  line-height: 1.6;
}
</style>
