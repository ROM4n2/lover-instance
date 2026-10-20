import { createApp } from 'vue'
import ElementPlus from 'element-plus'
import 'element-plus/dist/index.css'
import App from './App.vue'
import './style.css'

// No router: the dashboard's "route" is a query string (?view=&talker=), see
// useUrlState.js. That keeps refresh, bookmarks and back/forward working with
// nothing more than the History API.
createApp(App).use(ElementPlus).mount('#app')
