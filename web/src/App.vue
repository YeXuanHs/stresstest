<template>
  <div class="app-container">
    <div class="background-image"></div>
    <router-view />
  </div>
</template>

<script setup>
</script>

<style>
* {
  margin: 0;
  padding: 0;
  box-sizing: border-box;
}

html, body {
  width: 100%;
  height: 100%;
  font-family: 'Microsoft YaHei', 'PingFang SC', sans-serif;
}

.app-container {
  width: 100%;
  min-height: 100vh;
  position: relative;
  overflow: hidden;
}

.background-image {
  position: fixed;
  top: 0;
  left: 0;
  width: 100%;
  height: 100%;
  background: url('https://loliapi.com/acg') center center / cover no-repeat;
  z-index: -1;
  filter: blur(2px);
  -webkit-filter: blur(2px);
}

/* 全局 Element Plus 样式覆盖 */
.el-card {
  background: rgba(255, 255, 255, 0.6) !important;
  border: 1px solid rgba(255, 255, 255, 0.3) !important;
  border-radius: 12px !important;
  backdrop-filter: blur(20px);
  -webkit-backdrop-filter: blur(20px);
  box-shadow: 0 8px 32px rgba(0, 0, 0, 0.1) !important;
}

.el-button--primary {
  background-color: #409eff !important;
  border-color: #409eff !important;
  border-radius: 8px !important;
}

.el-input__wrapper {
  border-radius: 8px !important;
}

.el-select .el-input__wrapper {
  border-radius: 8px !important;
}

.el-dialog {
  border-radius: 12px !important;
}
</style>
