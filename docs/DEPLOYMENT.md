# Deploying OptiRoute to AWS (single EC2 server)

This is how the first public OptiRoute deployment was built, step by step, with the reason for each step.
It targets a small budget (an AWS Free plan account with $100 of credits) and runs the whole stack on one server.

Values specific to the live deployment are written as placeholders:

| Placeholder | Meaning |
|---|---|
| `<ACCOUNT_ID>` | AWS account ID |
| `<ADMIN_IP>` | Your own public IP, allowed to SSH |
| `<ALERT_EMAIL>` | Address for budget alerts |
| `<EIP>` | Elastic IP of the server, e.g. `13.233.182.99` |
| `<EIP_DASHED>` | The same IP with dashes, e.g. `13-233-182-99` |
| `<VPC_ID>`, `<SG_ID>`, `<INSTANCE_ID>`, `<ALLOC_ID>` | IDs AWS returns when the resources are created |

## The big picture

```
                    Internet (browsers, phones)
                              │  HTTPS :443  (HTTP :80 redirects to HTTPS)
                              ▼
 ┌──────────── EC2 t3.small · Ubuntu 24.04 · <EIP> ─────────────────────┐
 │  Caddy (web server + automatic HTTPS)                                │
 │    ├── /api/*  ──────►  Go API  :8080        (systemd service)       │
 │    └── everything else ► /var/www/optiroute (React dashboard files)  │
 │                                                                      │
 │  Optimizer worker (Python, systemd) ◄── jobs via Redis Streams ──┐   │
 │                                                                  │   │
 │  Docker, reachable only from inside the server (127.0.0.1):      │   │
 │    Postgres+PostGIS :15432   Redis :16379 ───────────────────────┘   │
 │    OSRM :15000 (Odisha road graph)                                   │
 └──────────────────────────────────────────────────────────────────────┘
```

Everything runs on one server. The internet can reach only ports 22 (SSH, from `<ADMIN_IP>` only), 80 and 443.
Postgres, Redis and OSRM are never exposed.

---

## Phase 1: Choosing the hosting

| Option | Verdict | Why |
|---|---|---|
| Cloudflare | No, for the backend | Workers can't run a long-lived Go server or native OR-Tools, and there is no PostGIS or Redis. Only the dashboard would fit (Pages). |
| Railway | Works, $15–25/mo | Runs containers well, but OSRM's always-on RAM makes it costly. |
| **AWS EC2, one server** | **Chosen** | Covered by the credits, and the same setup as the local `docker-compose`. |

**Picking the instance type.** Free plan accounts can only launch certain types. Ask AWS which:

```bash
aws ec2 describe-instance-types --region ap-south-1 \
  --filters Name=free-tier-eligible,Values=true
```

At the time this returned t3.micro/small, t4g.*, t8i.*, c7i-flex.large and m7i-flex.large.

- **ARM (t4g) is ruled out:** the `postgis/postgis` Docker image is only published for x86.
- **micro types are ruled out:** 1 GB of RAM is too little for OSRM + Postgres + the solver.
- **t3.small** (2 vCPU, 2 GB) was chosen: $0.0224/hour in Mumbai, about $16 a month.

Monthly cost: server ~$16 + 30 GB gp3 disk ~$2.50 + public IPv4 ~$3.60 ≈ **$22**.

---

## Phase 2: AWS credentials on the laptop

```bash
aws configure set region ap-south-1 --profile optiroute   # aws login fails without a region
aws login --profile optiroute                              # browser sign-in; stores short-lived credentials
aws sts get-caller-identity --profile optiroute            # "who am I?" — check before creating anything
```

`aws login` gives short-lived credentials, which is safer than long-lived access keys (never create root access keys).
Prefer an IAM user with admin rights and MFA over the root user for day-to-day work.

All commands below assume `export AWS_PROFILE=optiroute`.

---

## Phase 3: Read-only checks before creating anything

| Check | Command |
|---|---|
| Default VPC exists | `aws ec2 describe-vpcs --filters Name=is-default,Values=true` |
| Nothing already running | `aws ec2 describe-instances` and `aws ec2 describe-addresses` |
| Existing key pairs | `aws ec2 describe-key-pairs` |
| Latest Ubuntu 24.04 image | `aws ssm get-parameter --name /aws/service/canonical/ubuntu/server/24.04/stable/current/amd64/hvm/ebs-gp3/ami-id` |
| Your public IP | `curl https://checkip.amazonaws.com` |

The SSM parameter is a pointer Canonical keeps updated to the newest Ubuntu image, so the image ID is never hard-coded.

---

## Phase 4: Creating the AWS resources

### 4.1 Budget alert (first, so spending can't surprise you)

```bash
aws budgets create-budget --account-id <ACCOUNT_ID> \
  --budget '{"BudgetName":"optiroute-monthly","BudgetLimit":{"Amount":"25","Unit":"USD"},"TimeUnit":"MONTHLY","BudgetType":"COST"}' \
  --notifications-with-subscribers '[
    {"Notification":{"NotificationType":"ACTUAL","ComparisonOperator":"GREATER_THAN","Threshold":80,"ThresholdType":"PERCENTAGE"},
     "Subscribers":[{"SubscriptionType":"EMAIL","Address":"<ALERT_EMAIL>"}]},
    {"Notification":{"NotificationType":"FORECASTED","ComparisonOperator":"GREATER_THAN","Threshold":100,"ThresholdType":"PERCENTAGE"},
     "Subscribers":[{"SubscriptionType":"EMAIL","Address":"<ALERT_EMAIL>"}]}]'
```

- **ACTUAL 80%** emails once $20 has really been spent in a month.
- **FORECASTED 100%** emails early, when AWS predicts the month will pass $25.

### 4.2 SSH key pair

```bash
ssh-keygen -t ed25519 -N "" -C optiroute-ec2 -f ~/.ssh/optiroute-ec2      # on the laptop
aws ec2 import-key-pair --key-name optiroute-ec2 \
  --public-key-material fileb://~/.ssh/optiroute-ec2.pub
```

The private key never leaves the laptop; only the public half goes to AWS, which installs it on the server.
(If AWS generates the key, the private key travels over the network once.)

### 4.3 Security group (the server's firewall)

```bash
aws ec2 create-security-group --group-name optiroute \
  --description "OptiRoute server" --vpc-id <VPC_ID>               # → <SG_ID>
aws ec2 authorize-security-group-ingress --group-id <SG_ID> --ip-permissions \
  'IpProtocol=tcp,FromPort=22,ToPort=22,IpRanges=[{CidrIp=<ADMIN_IP>/32}]' \
  'IpProtocol=tcp,FromPort=80,ToPort=80,IpRanges=[{CidrIp=0.0.0.0/0}],Ipv6Ranges=[{CidrIpv6=::/0}]' \
  'IpProtocol=tcp,FromPort=443,ToPort=443,IpRanges=[{CidrIp=0.0.0.0/0}],Ipv6Ranges=[{CidrIpv6=::/0}]'
```

`/32` means exactly one address; `0.0.0.0/0` means everyone. Anything not listed is blocked.

### 4.4 Launching the server

```bash
aws ec2 run-instances --image-id <AMI_ID> --instance-type t3.small \
  --key-name optiroute-ec2 --security-group-ids <SG_ID> \
  --metadata-options HttpTokens=required,HttpEndpoint=enabled \
  --block-device-mappings 'DeviceName=/dev/sda1,Ebs={VolumeSize=30,VolumeType=gp3,Encrypted=true,DeleteOnTermination=true}' \
  --user-data file://bootstrap.sh \
  --tag-specifications 'ResourceType=instance,Tags=[{Key=Name,Value=optiroute}]'
# → <INSTANCE_ID>
```

| Option | Meaning |
|---|---|
| `HttpTokens=required` | Enforces IMDSv2, the safer instance metadata service; blocks a known class of credential-stealing attacks. |
| `gp3`, `Encrypted=true` | Current, cheaper disk type; encryption at rest is free. |
| `DeleteOnTermination=true` | Deleting the server deletes its disk, so no orphaned disk keeps billing. |
| `--user-data` | A script that runs once, as root, on first boot (4.6). |

### 4.5 Elastic IP (a fixed public address)

```bash
aws ec2 allocate-address --domain vpc                                         # → <EIP>, <ALLOC_ID>
aws ec2 associate-address --instance-id <INSTANCE_ID> --allocation-id <ALLOC_ID>
```

Without it, the public IP changes on every stop/start, breaking the URL and the certificate.

### 4.6 First-boot script (`bootstrap.sh`, passed as user data)

```bash
#!/bin/bash
set -eux                                  # stop on error, print each command into the log
fallocate -l 4G /swapfile && chmod 600 /swapfile && mkswap /swapfile && swapon /swapfile
echo '/swapfile none swap sw 0 0' >> /etc/fstab        # keep swap after reboots
apt-get update && apt-get install -y git caddy rsync
curl -fsSL https://get.docker.com | sh                 # Docker's official installer
usermod -aG docker ubuntu                              # let ubuntu run docker without sudo
sudo -u ubuntu bash -c 'curl -LsSf https://astral.sh/uv/install.sh | sh'   # uv, the Python package manager
touch /var/lib/optiroute-bootstrap-done                # marker: "finished"
```

- **Swap** is disk used as extra memory; with 2 GB of RAM it stops large solves from crashing the server.
- Wait for `/var/lib/optiroute-bootstrap-done` before continuing. The script's log is `/var/log/cloud-init-output.log`.

---

## Phase 5: Configuring the app on the server

```bash
ssh -i ~/.ssh/optiroute-ec2 ubuntu@<EIP>
git clone https://github.com/patiabhishek123/OptiRoute.git ~/OptiRoute
cd ~/OptiRoute
PW=$(openssl rand -hex 24)                         # random DB password
JWT=$(openssl rand -base64 48 | tr -d "\n")        # random secret for signing login tokens
sed -e "s|^POSTGRES_PASSWORD=.*|POSTGRES_PASSWORD=$PW|" \
    -e "s|optiroute:optiroute@|optiroute:$PW@|" \
    -e "s|^JWT_SECRET=.*|JWT_SECRET=$JWT|" \
    -e "/^OSRM_MEM_LIMIT=/d" .env.example > .env
cat >> .env <<EOF
MIGRATE_ON_START=true
OSRM_DATASET_VERSION_FILE=/home/ubuntu/OptiRoute/services/routing/osrm/data/DATASET_VERSION
OSRM_MEM_LIMIT=1200m
EOF
chmod 600 .env
```

- Secrets are generated on the server, so they never pass through a laptop or chat.
- `MIGRATE_ON_START=true` makes the API apply database migrations when it starts.
- `OSRM_MEM_LIMIT` is lowered from the dev default of 2g to fit the 2 GB server.

---

## Phase 6: Building on the laptop and uploading

Compiling Go and building the React app needs tools and RAM the server doesn't need to carry, so build once and ship the results.

```bash
# A temporary second checkout of the release, leaving the working copy untouched
git worktree add --detach /tmp/optiroute-deploy origin/main

# Go API → one self-contained Linux binary
cd /tmp/optiroute-deploy/services/backend
CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -trimpath -o /tmp/optiroute-api ./cmd/api

# Dashboard → static files in dist/
cd /tmp/optiroute-deploy/apps/dashboard && npm ci && npm run build

# Upload (rsync sends only changes; -z compresses)
SSH="ssh -i $HOME/.ssh/optiroute-ec2"
rsync -az -e "$SSH" /tmp/optiroute-api ubuntu@<EIP>:~/optiroute-api
rsync -az --delete -e "$SSH" dist/ ubuntu@<EIP>:~/dashboard/
cd ~/Programs/OptiRoute/services/routing/osrm/data
rsync -az -e "$SSH" odisha.osrm.* DATASET_VERSION ubuntu@<EIP>:~/OptiRoute/services/routing/osrm/data/

git worktree remove --force /tmp/optiroute-deploy
```

- `GOOS`/`GOARCH` cross-compile for Linux x86; `CGO_ENABLED=0` makes the binary fully self-contained.
- Building the OSRM graph needs ~3 GB of RAM, more than the server has. Build it locally with `make osrm-build`
  and copy only the finished `odisha.osrm.*` files (~430 MB), not the raw `.osm.pbf` downloads.

---

## Phase 7: Postgres, Redis and OSRM (Docker)

Don't edit the repo's `docker-compose.yml`. Compose automatically merges a file named `docker-compose.override.yml`,
so add one on the server only (it is not in git):

```yaml
services:
  db: { restart: unless-stopped }       # come back after a reboot or crash
  redis: { restart: unless-stopped }
  osrm: { restart: unless-stopped }
```

```bash
docker compose --profile routing up -d --wait   # --profile routing includes OSRM; --wait blocks until healthy
docker stats --no-stream                         # roughly: osrm ~300 MB, db ~50 MB, redis ~7 MB
```

The compose file publishes ports as `127.0.0.1:15432:5432`: reachable only from the server itself,
a second layer of protection behind the security group.

If `docker` says permission denied right after bootstrap, the docker group isn't active in your session yet.
Log out and back in, or prefix one command with `sg docker -c "..."`.

---

## Phase 8: API and worker as systemd services

systemd starts programs at boot, restarts them when they crash and keeps their logs.

`/etc/systemd/system/optiroute-api.service`:

```ini
[Unit]
Description=OptiRoute API
After=docker.service network-online.target

[Service]
User=ubuntu
EnvironmentFile=/home/ubuntu/OptiRoute/.env
ExecStart=/home/ubuntu/optiroute-api
Restart=always
RestartSec=3

[Install]
WantedBy=multi-user.target
```

`/etc/systemd/system/optiroute-worker.service`:

```ini
[Unit]
Description=OptiRoute optimizer worker
After=docker.service network-online.target

[Service]
User=ubuntu
WorkingDirectory=/home/ubuntu/OptiRoute/services/optimizer
EnvironmentFile=/home/ubuntu/OptiRoute/.env
ExecStart=/home/ubuntu/.local/bin/uv run --no-sync optiroute worker
Restart=always
RestartSec=3

[Install]
WantedBy=multi-user.target
```

```bash
cd ~/OptiRoute/services/optimizer && ~/.local/bin/uv sync --frozen --no-dev   # exact locked deps, no test tools
chmod +x ~/optiroute-api
sudo systemctl daemon-reload                                 # read the new unit files
sudo systemctl enable --now optiroute-api optiroute-worker   # start now and at boot
journalctl -u optiroute-api -u optiroute-worker              # expect "api listening" and "consuming opti:jobs"
```

---

## Phase 9: Public website with HTTPS (Caddy)

```bash
sudo mkdir -p /var/www/optiroute && sudo rsync -a --delete ~/dashboard/ /var/www/optiroute/
```

`/etc/caddy/Caddyfile`:

```
<EIP_DASHED>.sslip.io {
	encode gzip
	handle /api/* {
		reverse_proxy localhost:8080
	}
	handle {
		root * /var/www/optiroute
		try_files {path} /index.html
		file_server
	}
}
```

```bash
sudo systemctl reload caddy
sudo journalctl -u caddy | grep "certificate obtained"
```

- **sslip.io** is free DNS: `<EIP_DASHED>.sslip.io` always resolves to `<EIP>`, so no domain purchase is needed.
  With a real domain, point an A record at `<EIP>` and use that name in the Caddyfile instead.
- **Automatic HTTPS:** for any hostname in the Caddyfile, Caddy gets a Let's Encrypt certificate and renews it.
- **`/api/*` goes to the API** on the same origin (as the Vite dev proxy does locally), so no CORS setup is needed.
- **`try_files ... /index.html`** lets React Router handle deep links like `/plans/123`.

---

## Phase 10: Checking it works

| Check | Expected |
|---|---|
| `curl -s -o /dev/null -w '%{http_code}' https://<EIP_DASHED>.sslip.io/` | `200` |
| `curl https://<EIP_DASHED>.sslip.io/api/v1/organizations/me` | `401 missing bearer token` (API up, login required) |
| `python3 scripts/e2e_api.py` on the server | `M3 end-to-end: OK` (signup → fleet → 100 orders → OSRM solve → plan → approve → activate) |

The e2e script creates a test organisation ("E2E Couriers"); delete it afterwards (Phase 12).

**Corporate networks:** a firewall that inspects HTTPS (Fortinet in our case) re-signs certificates, so `curl` from
that network fails with `unable to get local issuer certificate`. `openssl s_client -connect <EIP>:443` shows the
real issuer. Test from the server itself or another network to tell this apart from a server problem.

---

## Phase 11: Releasing a new version

```bash
# 1. Quality gate on the laptop (start local infra so DB-backed tests don't skip)
make up && make db-migrate && make lint test && make down

# 2. Merge the release (only when a milestone is ready) and push
git switch main && git merge --no-ff development && git push origin main && git switch development

# 3. Build the new API binary as in Phase 6, upload it as ~/optiroute-api.new
#    (rebuild and rsync the dashboard too if apps/dashboard changed)

# 4. On the server
cd ~/OptiRoute
mkdir -p ~/backups
docker compose exec -T db pg_dump -U optiroute -Fc optiroute > ~/backups/pre-$(git rev-parse --short origin/main).dump
git pull --ff-only
(cd services/optimizer && ~/.local/bin/uv sync --frozen --no-dev)
sudo systemctl stop optiroute-api
mv ~/optiroute-api ~/optiroute-api.prev && mv ~/optiroute-api.new ~/optiroute-api && chmod +x ~/optiroute-api
sudo systemctl start optiroute-api            # applies any new migrations on start
sudo systemctl restart optiroute-worker
journalctl -u optiroute-api --since "-1min" | grep -E "migration|listening"
```

- **Back up before a release:** migrations change tables; the dump is the way back.
- `--ff-only` refuses to pull if the server's copy has diverged, instead of silently merging.
- `~/optiroute-api.prev` is kept for an instant rollback.

---

## Phase 12: Deleting an organisation's data

Take a backup first. Run the backup and `psql` as **separate** commands: when SQL is fed through stdin to a chain
like `docker compose exec ... pg_dump ... && docker compose exec ... psql`, the first `exec` reads the stdin and
`psql` gets nothing.

`DELETE FROM core.organizations` alone fails: the composite foreign key from `routing.routes` to
`operations.vehicles` is not a cascade, and Postgres can reach the vehicles before the routes. Delete children
first, in one transaction:

```sql
BEGIN;
CREATE TEMP TABLE t(id uuid) ON COMMIT DROP;
INSERT INTO t SELECT id FROM core.organizations WHERE name = 'E2E Couriers';
UPDATE optimization.jobs SET parent_route_plan_id = NULL WHERE organization_id IN (SELECT id FROM t);  -- breaks the jobs↔plans cycle
DELETE FROM operations.gps_events         WHERE organization_id IN (SELECT id FROM t);
DELETE FROM routing.order_assignments     WHERE organization_id IN (SELECT id FROM t);
DELETE FROM routing.route_stops           WHERE organization_id IN (SELECT id FROM t);
DELETE FROM routing.routes                WHERE organization_id IN (SELECT id FROM t);
DELETE FROM routing.route_plans           WHERE organization_id IN (SELECT id FROM t);
DELETE FROM optimization.jobs             WHERE organization_id IN (SELECT id FROM t);
DELETE FROM operations.driver_assignments WHERE organization_id IN (SELECT id FROM t);
DELETE FROM operations.orders             WHERE organization_id IN (SELECT id FROM t);
DELETE FROM operations.vehicles           WHERE organization_id IN (SELECT id FROM t);
DELETE FROM operations.drivers            WHERE organization_id IN (SELECT id FROM t);
DELETE FROM core.users                    WHERE organization_id IN (SELECT id FROM t);
DELETE FROM core.depots                   WHERE organization_id IN (SELECT id FROM t);
DELETE FROM core.organizations            WHERE id IN (SELECT id FROM t);
COMMIT;
```

Afterwards, published `core.outbox` rows whose `payload->>'job_id'` points at a deleted job can be removed too.

---

## Day-to-day commands

```bash
ssh -i ~/.ssh/optiroute-ec2 ubuntu@<EIP>

# Status and logs
systemctl status optiroute-api optiroute-worker caddy
journalctl -u optiroute-api -f
cd ~/OptiRoute && docker compose ps
free -h; df -h

# Restart a piece
sudo systemctl restart optiroute-api
docker compose restart osrm

# Roll back the API binary
sudo systemctl stop optiroute-api && mv ~/optiroute-api.prev ~/optiroute-api && sudo systemctl start optiroute-api

# Restore a backup (destructive: replaces current data)
docker compose exec -T db pg_restore -U optiroute -d optiroute --clean --if-exists < ~/backups/<file>.dump

# Close public signup once your account exists
echo ALLOW_SIGNUP=false >> ~/OptiRoute/.env && sudo systemctl restart optiroute-api
```

- **SSH times out:** your IP probably changed. Update the port 22 rule of the `optiroute` security group.
- **Saving credits:** stop the instance from the EC2 console when not needed. The disk and Elastic IP still cost
  about $6 a month while stopped.

---

## Next improvements

1. **Off-server backups:** daily `pg_dump` uploaded to S3, or EBS snapshots with Data Lifecycle Manager.
2. **Stop using root:** an IAM admin user with MFA for everyday work.
3. **Dockerfiles and CI deploys:** build API/worker images and deploy from GitHub Actions instead of copying binaries.
4. **Monitoring:** CloudWatch alarms on CPU and status checks; an uptime check on the URL.
5. **A real domain** instead of sslip.io.
