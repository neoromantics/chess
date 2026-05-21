# 10. What "the cloud" buys you (and what it costs)

This system runs on a single VM at Duke. That's deliberate — it's free, it's enough for a school project, and one operator reads `kubectl logs` directly. But "deploy to AWS" comes up in every interview, so it's worth understanding what that phrase actually means for *this* stack.

## 10.1 What AWS actually is

A buffet of ~200 managed services. You don't "deploy to AWS" wholesale — you pick which pieces of your infrastructure to swap for AWS-operated versions. The pitch on each one: instead of running Postgres yourself (backups, replication, failover, patching), pay AWS to run it and treat it as a hostname-and-credentials. Same for Redis, k8s control plane, DNS, certificates, secrets, load balancers, CDN, registry.

GCP and Azure offer near-identical menus with different names. Picking between them is mostly a function of existing org relationships, not capability.

## 10.2 Mapping our current stack to AWS

| What we run today | What AWS gives us | What the handoff buys |
|---|---|---|
| One Duke VM | EC2 Auto Scaling Group, or EKS-managed nodes | Cluster spans multiple AZs (independent datacenters in one region) — one DC failing doesn't take us down |
| k3s, we admin the control plane | EKS (managed k8s) | AWS runs etcd, the API server, the scheduler |
| Postgres as a k8s Deployment + PVC | RDS PostgreSQL with Multi-AZ | Synchronous standby in another AZ, automatic failover (~60s), daily snapshots + 5-min point-in-time recovery, optional read replicas for `ListGames`/search |
| Redis as a k8s Deployment + PVC | ElastiCache for Redis | Replication + automatic failover (replaces the "Redis Sentinel" roadmap item), automated snapshots |
| Traefik + Let's Encrypt | ALB + ACM | Free auto-renewing TLS certs, native WebSocket support, integrates with WAF |
| `go:embed` SPA into the gateway binary | S3 + CloudFront (CDN) | SPA served from edge POPs globally; gateway image stops growing when frontend changes; SPA-only deploys don't touch k8s |
| `chess-secrets` k8s Secret | Secrets Manager / Parameter Store | Auto-rotation, audit log, KMS encryption at rest |
| ghcr.io for images | ECR | Faster pulls from EKS, vulnerability scanning, IAM-integrated |
| `kubectl logs` | CloudWatch Logs (or keep Prometheus) | Searchable retention, log-based alerts — at the cost of CloudWatch's mediocre UX |
| No DR plan | RDS snapshots + cross-region replication | An actual disaster-recovery story |
| No DNS we own | Route 53 | We own the domain. Today we're on `vcm-50800.vm.duke.edu` — when Duke takes the VM back, the domain goes with it |

## 10.3 What it specifically fixes for our system

Walk through the failure modes called out in [`../docs/invariants.md`](../docs/invariants.md) and the roadmap:

1. **Redis is a SPOF.** Today, if Redis dies, every cache read misses, every live update drops, matchmaking queues vanish. AOF gives durability on disk but not failover. **ElastiCache with a replica** gives automatic failover in ~30s. Biggest reliability win — it's been on the roadmap as "Redis Sentinel" forever, deferred because we don't have a second node.
2. **Postgres is a SPOF.** Same problem, worse consequences (source of truth). **RDS Multi-AZ** gives a synchronous standby in another AZ and ~60s failover.
3. **The whole VM is a SPOF.** Duke pulls the VM offline for maintenance → cluster gone. **EKS across 3 AZs** means an entire AZ can fail and the cluster keeps serving.
4. **No backup story.** It's not actually clear what would happen if `chess-db-pvc` corrupted right now. **RDS** does daily snapshots + 5-min PITR built in, zero code.
5. **Engine search bursts are painful to scale out.** Today an analysis storm fills our 8-pod HPA ceiling on the VM. **EKS + Spot instances + Cluster Autoscaler** lets engine-worker scale to 50+ pods for a 30-min burst, then scale back to 2. Spot pricing is ~30% of on-demand, and engine-worker is the textbook Spot workload (interruptible, parallel, stateless).
6. **Frontend bundle ships with every gateway change.** Every backend tweak rebuilds the SPA and bloats the Docker image. **S3 + CloudFront** decouples the SPA — SPA-only changes deploy in seconds without touching k8s.
7. **Pod-level secrets are coarse.** Everything in `chess-secrets` is visible to every pod that mounts it. **IRSA (IAM Roles for Service Accounts)** gives per-pod credentials — game-service has a role that can read game tables; gateway has a separate role for user tables. Least privilege without a secret-per-service.
8. **TLS cert renewal could break.** Let's Encrypt is great until it isn't (rate limits, DNS challenge weirdness). **ACM** is "click yes" and AWS handles it forever.

## 10.4 What you give up

In priority order — be honest about these in any interview:

1. **Cost.** Duke VM = $0/month. A barebones equivalent AWS footprint for our scale: EKS control plane (~$73/mo) + 3× t3.medium nodes (~$90/mo) + RDS db.t4g.small Multi-AZ (~$60/mo) + ElastiCache cache.t3.micro with a replica (~$25/mo) + ALB (~$20/mo + traffic) + Route 53 + S3 + data transfer. Realistic floor for *barebones* prod is **$300–500/month**. For a hobby project, that doesn't change.
2. **Complexity surface.** EKS is harder than k3s. The IAM + VPC + Security Group + Subnet design is a multi-day project to get right. AWS-flavored networking (private vs public subnets, NAT gateways, VPC peering, endpoint policies) is its own discipline. For 3 services this is overkill; for 30 it's table stakes.
3. **Vendor lock-in by tier.** Some AWS services are "standard X with a billing relationship" — RDS Postgres is plain Postgres, `pg_dump` and walk away. Some are AWS-specific — DynamoDB, SQS, CloudWatch alarms, IAM policies. The further down the stack you go (managed DBs → app services → IAM model), the harder it is to leave. Stay close to open standards if you care about portability. `EKS + RDS Postgres + ElastiCache Redis` is portable; `EKS + DynamoDB + SQS + EventBridge + Lambda` is not.
4. **Different debugging surface.** `kubectl logs` still works, but incident response moves to CloudWatch dashboards, X-Ray traces, IAM audit logs. The mental model shifts from "SSH into a box and grep" to "click through five web consoles."
5. **Simple things become forms with thirty fields.** "Add a Postgres user" goes from `CREATE USER` to IAM role + RDS auth config + database role + grant matrix + secret rotation policy. For our scale this is friction; at fintech scale it's compliance.

## 10.5 The mental shift

AWS doesn't make our application better. It makes our **failure modes** better — and only for the failure modes we're willing to pay for. The single-VM Duke setup is *correct* for a school project with one operator reading `kubectl logs`. The moment a real userbase would notice if the platform went down for an afternoon, AWS (or GCP, or Azure — substitutes for this purpose) starts paying off.

The framing to carry: **AWS is a list of "things I no longer have to operate."** Each service is a tradeoff — money + a bit of opacity, in exchange for reliability + scale + features you'd otherwise build yourself. Picking *all of them* is how startups burn through a seed round on a 100-user app.

For *this* system, the high-leverage moves, in order, would be: RDS for Postgres (fixes backups + Multi-AZ) → ElastiCache for Redis (fixes the SPOF) → ALB + ACM (removes Let's Encrypt operational risk) → S3 + CloudFront for the SPA (decouples frontend deploys). Everything else — EKS vs self-hosted k3s on EC2, IRSA, CloudWatch vs Prometheus — is secondary and largely a matter of taste.

---

← [`09-deep-dives/`](09-deep-dives/) · Next: [`11-production-grade.md`](11-production-grade.md)
