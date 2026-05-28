<template>
  <div class="legal-view">
    <h1>About</h1>
    <section>
      <p>
        <strong>Chess</strong> is a modern, multiplayer chess platform built with a focus on 
        performance, reliability, and real-time interaction.
      </p>
      <p>
        This project was created by <strong>Taiyan Liu</strong> (GitHub: 
        <a href="https://github.com/neoromantics" target="_blank">neoromantics</a>), 
        a recent graduate of <strong>Duke University</strong>. 
      </p>
      <p>
        The platform features:
      </p>
      <ul>
        <li>Server-authoritative game logic and clocks.</li>
        <li>Distributed engine offloading for move analysis.</li>
        <li>Real-time gameplay via WebSockets.</li>
        <li>Matchmaking and Glicko-2 ratings.</li>
      </ul>
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
a {
  color: #4a6b8a;
  text-decoration: none;
}
a:hover {
  text-decoration: underline;
}
</style>
