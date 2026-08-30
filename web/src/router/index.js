import { createRouter, createWebHistory } from 'vue-router'
import api from '../api'

const routes = [
  { path: '/', name: 'Home', component: () => import('../views/Dashboard.vue') },
  { path: '/setup', name: 'Setup', component: () => import('../views/Setup.vue') },
  { path: '/login', name: 'Login', component: () => import('../views/Login.vue') },
  { path: '/dashboard', name: 'Dashboard', component: () => import('../views/Dashboard.vue') },
  { path: '/agent', name: 'AgentPanel', component: () => import('../views/AgentPanel.vue') },
  { path: '/settings', name: 'Settings', component: () => import('../views/Settings.vue') },
  { path: '/help', name: 'Help', component: () => import('../views/Help.vue') }
]

const router = createRouter({
  history: createWebHistory(),
  routes
})

let serverConfig = null

async function getServerConfig() {
  if (serverConfig) return serverConfig
  try {
    const res = await api.get('/api/mode')
    serverConfig = { mode: res.mode || '', hasPassword: res.has_password || false }
  } catch {
    serverConfig = { mode: '', hasPassword: false }
  }
  return serverConfig
}

export function resetConfig() {
  serverConfig = null
}

router.beforeEach(async (to, from, next) => {
  // 设置页和帮助页直接放行
  if (to.path === '/setup' || to.path === '/help') {
    return next()
  }

  // 检查配置
  const config = await getServerConfig()
  const needSetup = !config.mode || !config.hasPassword

  // 需要设置，跳转到设置页
  if (needSetup) {
    return next('/setup')
  }

  // 根据模式确定默认页
  const defaultPage = config.mode === 'agent' ? '/agent' : '/dashboard'

  // 根目录跳转到默认页
  if (to.path === '/') {
    return next(defaultPage)
  }

  // 检查登录
  if (to.path !== '/login') {
    const token = localStorage.getItem('token')
    if (!token) return next('/login')
  }

  // 已登录访问登录页，跳转到默认页
  if (to.path === '/login') {
    const token = localStorage.getItem('token')
    if (token) return next(defaultPage)
  }

  next()
})

export default router
