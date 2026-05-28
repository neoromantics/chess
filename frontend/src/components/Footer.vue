<template>
  <footer class="footer">
    <div class="footer-content">
      <div class="footer-left">
        <span class="copyright">&copy; {{ new Date().getFullYear() }} Taiyan Liu</span>
      </div>
      <div class="footer-right">
        <router-link to="/about" class="footer-link">About</router-link>
        <router-link to="/terms" class="footer-link">Terms of Service</router-link>
        <router-link to="/privacy" class="footer-link">Privacy Policy</router-link>
      </div>
    </div>
  </footer>
</template>

<style scoped>
.footer {
  background: #1a1a1a;
  border-top: 1px solid #333;
  padding: 20px 0;
  margin-top: 40px;
}
.footer-content {
  max-width: 1200px;
  margin: 0 auto;
  padding: 0 20px;
  display: flex;
  justify-content: space-between;
  align-items: center;
}
.footer-left {
  font-size: 13px;
  color: #888;
}
.footer-right {
  display: flex;
  gap: 20px;
}
.footer-link {
  font-size: 13px;
  color: #aaa;
  text-decoration: none;
  transition: color 0.2s;
}
.footer-link:hover {
  color: #fff;
}
@media (max-width: 600px) {
  .footer-content {
    flex-direction: column;
    gap: 10px;
    text-align: center;
  }
}
</style>
