<template>
  <div ref="host" class="echart" :style="{ height }"></div>
</template>

<script setup>
import { onMounted, onUnmounted, ref, watch } from 'vue'
import * as echarts from 'echarts'
// Registering the wordCloud series happens as a side effect of importing the
// plugin. It attaches to the same echarts module instance imported above, so
// the full build knows the series type. Imported here (not globally) so a
// view that never uses a word cloud still gets the same single bundle.
import 'echarts-wordcloud'

const props = defineProps({
  option: { type: Object, default: null },
  height: { type: String, default: '320px' },
})

const host = ref(null)
let chart = null

function render() {
  if (!chart || !props.option) return
  // notMerge=true so a view that switches between datasets does not leave the
  // previous series behind (e.g. the gating panel removing the trend line).
  chart.setOption(props.option, true)
}

function onResize() {
  if (chart) chart.resize()
}

onMounted(() => {
  chart = echarts.init(host.value)
  render()
  window.addEventListener('resize', onResize)
})

watch(() => props.option, render, { deep: true })

onUnmounted(() => {
  // Lifecycle guarantee: the resize listener and the ECharts instance are torn
  // down with the component, so switching views does not leak a canvas bound to
  // a detached node or a handler bound to a disposed chart.
  window.removeEventListener('resize', onResize)
  if (chart) {
    chart.dispose()
    chart = null
  }
})
</script>

<style scoped>
.echart {
  width: 100%;
}
</style>
