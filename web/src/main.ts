import { createApp } from 'vue'
import { installUI } from './ui'
import 'ant-design-vue/dist/reset.css'
import './style.css'
import App from './App.vue'

const app = createApp(App)
app.use(installUI)
app.mount('#app')
