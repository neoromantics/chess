# 3.1 "Works locally" is not enough

Your dev machine runs one copy of each service. Production runs *N* copies behind a load balancer, where N changes during the day as the autoscaler reacts to load. Two requests for the same game arrive at two different pods. Both pods think they own the truth. Without coordination, they trample each other.

The implication: every change has to survive

- **(a) multi-replica deployment** — N copies of each service all reading the same Redis and Postgres,
- **(b) rolling restarts** — during deploys we kill pods one at a time and bring up new ones, so for a few seconds traffic hits a mix of old and new code, and
- **(c) an HTTPS reverse proxy in front** — Traefik terminates TLS and forwards plain HTTP, which means anything that depends on the client's TCP socket or IP needs `X-Forwarded-For` handling.

When you write a new endpoint, ask yourself: *what happens if two replicas process this simultaneously? what happens if the pod dies mid-request? what happens if the request is retried?*

The rest of section 3 is mostly tools to answer those questions.

---

← [`README.md`](README.md) · Next: [`02-distributed-locks.md`](02-distributed-locks.md)
