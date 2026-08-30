import { defineStore } from 'pinia'
import { ref, computed } from 'vue'

export const useAppStore = defineStore('app', () => {
  const mode = ref('') // master / agent / both
  const token = ref('')
  const isLoggedIn = ref(false)
  const agents = ref([])
  const stats = ref({
    totalBytes: 0,
    totalPackets: 0,
    totalErrors: 0,
    uptime: ''
  })

  const isMaster = computed(() => mode.value === 'master' || mode.value === 'both')
  const isAgent = computed(() => mode.value === 'agent' || mode.value === 'both')

  function setMode(m) {
    mode.value = m
  }

  function setToken(t) {
    token.value = t
  }

  function setLoggedIn(v) {
    isLoggedIn.value = v
  }

  function setAgents(a) {
    agents.value = a
  }

  function setStats(s) {
    stats.value = s
  }

  return {
    mode,
    token,
    isLoggedIn,
    agents,
    stats,
    isMaster,
    isAgent,
    setMode,
    setToken,
    setLoggedIn,
    setAgents,
    setStats
  }
})
