<template>
  <div class="legal-view">
    <h1>Privacy Policy</h1>
    
    <section>
      <h2>1. Information We Collect</h2>
      <p>
        We collect minimal information required to provide the service:
      </p>
      <ul>
        <li><strong>Account Information:</strong> If you register, we store your username and a hashed version of your password.</li>
        <li><strong>Gameplay Data:</strong> We record chess moves, game history, and ratings to enable the platform's functionality.</li>
        <li><strong>Anonymous Sessions:</strong> For non-registered users, we use a temporary session ID stored in a cookie.</li>
      </ul>
    </section>

    <section>
      <h2>2. How We Use Information</h2>
      <p>
        Your data is used solely to:
      </p>
      <ul>
        <li>Authenticate your session.</li>
        <li>Maintain your game history and ratings.</li>
        <li>Provide real-time updates and notifications.</li>
      </ul>
    </section>

    <section>
      <h2>3. Data Security</h2>
      <p>
        We use industry-standard practices to protect your data, including bcrypt for password hashing 
        and secure WebSocket connections.
      </p>
    </section>

    <section>
      <h2>4. Third-Party Sharing</h2>
      <p>
        We do not sell, trade, or otherwise transfer your personally identifiable information to third parties.
      </p>
    </section>

    <section>
      <h2>5. Cookies</h2>
      <p>
        We use cookies for authentication purposes only. By using the service, you consent to the use of these cookies.
      </p>
    </section>
  </div>
</template>

<style scoped>
.legal-view {
  max-width: 800px;
  margin: 40px auto;
  padding: 0 20px;
  line-height: 1.6;
}
h1 {
  color: #fff;
  border-bottom: 1px solid #333;
  padding-bottom: 10px;
  margin-bottom: 20px;
}
h2 {
  color: #ddd;
  margin-top: 25px;
  margin-bottom: 10px;
}
p {
  margin-bottom: 15px;
}
ul {
  margin-bottom: 15px;
  padding-left: 20px;
}
li {
  margin-bottom: 5px;
}
</style>
