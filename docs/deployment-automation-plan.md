# CCattler Multi-Node Cluster: Vagrant + Ansible Deployment Plan

Automated provisioning of a local multi-node CCattler cluster using Vagrant for VM
lifecycle and Ansible for configuration management. Adapted from the
Vagrant+Ansible-for-Kubernetes pattern, reworked entirely around CCattler's
fact-based architecture, etcd store, and multi-process model (Phase 16).

---

## 1. Goal

Stand up a fully functional CCattler cluster on a single developer machine where:

- An **etcd** node provides the shared fact store.
- One or two **control plane** nodes run `cca server` (controllers + API).
- Two or three **worker** nodes run `cca agent` (node agent with observer/reconciler/reporter).
- A developer can `cca apply --store etcd` a `.ccl` file and watch instances spread
  across workers, validating scheduling, health checks, failure recovery, and mTLS
  end-to-end.

The entire cluster is disposable (`vagrant destroy -f`) and reproducible
(`vagrant up && ansible-playbook site.yml`).

---

## 2. Prerequisites

| Tool | Purpose | Install |
|------|---------|---------|
| Vagrant >= 2.4 | VM lifecycle | `brew install vagrant` |
| VirtualBox >= 7 or libvirt | Hypervisor | `brew install --cask virtualbox` or system package |
| Ansible >= 2.15 | Provisioning | `brew install ansible` or `pip install ansible` |
| Go >= 1.22 | Build CCattler binary | `brew install go` (or pre-build the binary) |
| etcd >= 3.5 | Fact store (installed by Ansible) | Ansible handles this |

For Apple Silicon Macs using VMware Fusion instead of VirtualBox, install the
`vagrant-vmware-desktop` plugin and substitute the provider blocks below.

---

## 3. Cluster Topology

```
  192.168.56.10          192.168.56.11          192.168.56.12
 +--------------+      +--------------+      +--------------+
 |   cca-etcd   |      |  cca-ctrl-1  |      |  cca-ctrl-2  |
 |              |      |              |      |  (optional)  |
 |  etcd v3.5   |      | cca server   |      | cca server   |
 |  port 2379   |      | --endpoints  |      | --endpoints  |
 |              |      |  cca-etcd    |      |  cca-etcd    |
 +--------------+      +--------------+      +--------------+
        |                     |                     |
        +---------------------+---------------------+
        |          private network 192.168.56.0/24
        +---------------------+---------------------+
        |                     |                     |
 +--------------+      +--------------+      +--------------+
 | cca-worker-1 |      | cca-worker-2 |      | cca-worker-3 |
 |              |      |              |      |  (optional)  |
 | cca agent    |      | cca agent    |      | cca agent    |
 | --node-id    |      | --node-id    |      | --node-id    |
 |  worker-1    |      |  worker-2    |      |  worker-3    |
 +--------------+      +--------------+      +--------------+
```

### Node Roles

| Node | IP | Role | Process | Resources |
|------|----|------|---------|-----------|
| cca-etcd | 192.168.56.10 | Fact store | etcd | 1 vCPU, 1 GB RAM |
| cca-ctrl-1 | 192.168.56.11 | Control plane | `cca server` | 1 vCPU, 1 GB RAM |
| cca-ctrl-2 | 192.168.56.12 | Control plane (HA) | `cca server` | 1 vCPU, 1 GB RAM |
| cca-worker-1 | 192.168.56.20 | Worker | `cca agent --node-id worker-1` | 2 vCPU, 2 GB RAM |
| cca-worker-2 | 192.168.56.21 | Worker | `cca agent --node-id worker-2` | 2 vCPU, 2 GB RAM |
| cca-worker-3 | 192.168.56.22 | Worker (optional) | `cca agent --node-id worker-3` | 2 vCPU, 2 GB RAM |

All nodes use a **host-only private network** (`192.168.56.0/24`) so VMs can
communicate without exposing services to the LAN. VirtualBox creates the
`vboxnet0` interface automatically.

---

## 4. Vagrantfile

```ruby
# -*- mode: ruby -*-
# Vagrantfile for a local CCattler cluster

ETCD_IP       = "192.168.56.10"
CTRL_IPS      = ["192.168.56.11", "192.168.56.12"]
WORKER_IPS    = ["192.168.56.20", "192.168.56.21", "192.168.56.22"]
BOX           = "ubuntu/jammy64"

# How many control-plane and worker nodes to actually start.
# Reduce these for a lighter cluster.
NUM_CTRL      = 1   # 1 or 2
NUM_WORKERS   = 2   # 2 or 3

Vagrant.configure("2") do |config|

  # ---------- etcd node ----------
  config.vm.define "cca-etcd" do |node|
    node.vm.box      = BOX
    node.vm.hostname  = "cca-etcd"
    node.vm.network "private_network", ip: ETCD_IP
    node.vm.provider "virtualbox" do |vb|
      vb.memory = 1024
      vb.cpus   = 1
      vb.name   = "cca-etcd"
    end
  end

  # ---------- control-plane nodes ----------
  (1..NUM_CTRL).each do |i|
    config.vm.define "cca-ctrl-#{i}" do |node|
      node.vm.box      = BOX
      node.vm.hostname  = "cca-ctrl-#{i}"
      node.vm.network "private_network", ip: CTRL_IPS[i - 1]
      node.vm.provider "virtualbox" do |vb|
        vb.memory = 1024
        vb.cpus   = 1
        vb.name   = "cca-ctrl-#{i}"
      end
    end
  end

  # ---------- worker nodes ----------
  (1..NUM_WORKERS).each do |i|
    config.vm.define "cca-worker-#{i}" do |node|
      node.vm.box      = BOX
      node.vm.hostname  = "cca-worker-#{i}"
      node.vm.network "private_network", ip: WORKER_IPS[i - 1]
      node.vm.provider "virtualbox" do |vb|
        vb.memory = 2048
        vb.cpus   = 2
        vb.name   = "cca-worker-#{i}"
      end

      # Trigger Ansible after the last worker is up, so all hosts
      # are reachable when provisioning runs.
      if i == NUM_WORKERS
        node.vm.provision "ansible" do |ansible|
          ansible.playbook       = "ansible/site.yml"
          ansible.inventory_path = "ansible/inventory.ini"
          ansible.limit          = "all"
        end
      end
    end
  end
end
```

Workers get more resources (2 vCPU, 2 GB) because they run actual container
workloads via containerd. The etcd and control-plane nodes are lightweight.

---

## 5. Ansible Layout

```
ansible/
  ansible.cfg
  inventory.ini
  site.yml                    # master playbook (imports role playbooks)
  group_vars/
    all.yml                   # shared variables (etcd endpoints, Go version, etc.)
  roles/
    common/                   # Go runtime, build CCattler binary, base packages
      tasks/main.yml
    etcd/                     # install and configure etcd
      tasks/main.yml
      templates/etcd.service.j2
    certificates/             # generate CCattler internal CA + node certs
      tasks/main.yml
      templates/
    controlplane/             # cca server systemd unit
      tasks/main.yml
      templates/cca-server.service.j2
    worker/                   # cca agent systemd unit
      tasks/main.yml
      templates/cca-agent.service.j2
```

### 5.1 Inventory

```ini
# ansible/inventory.ini

[etcd]
cca-etcd ansible_host=192.168.56.10

[controlplane]
cca-ctrl-1 ansible_host=192.168.56.11
# cca-ctrl-2 ansible_host=192.168.56.12   # uncomment for HA

[workers]
cca-worker-1 ansible_host=192.168.56.20 node_id=worker-1
cca-worker-2 ansible_host=192.168.56.21 node_id=worker-2
# cca-worker-3 ansible_host=192.168.56.22 node_id=worker-3

[ccattler:children]
controlplane
workers

[all:vars]
ansible_user=vagrant
ansible_ssh_private_key_file=.vagrant/machines/{{ inventory_hostname }}/virtualbox/private_key
```

### 5.2 Shared Variables

```yaml
# ansible/group_vars/all.yml

go_version: "1.22.5"
etcd_version: "3.5.14"

# CCattler source — path on the Ansible controller (your laptop).
# Ansible will sync this to each VM and build the binary.
ccattler_src: "{{ playbook_dir }}/../../"   # points to repo root

# etcd connection
etcd_endpoints: "http://192.168.56.10:2379"

# Certificate settings
cca_cert_dir: /etc/ccattler/pki
cca_ca_validity: "8760h"        # 1 year for the CA
cca_cert_validity: "1h"         # short-lived leaf certs, auto-rotated

# Runtime adapter for workers
cca_runtime: "container"        # or "process" for lighter testing
```

### 5.3 Master Playbook

```yaml
# ansible/site.yml

---
- name: Common setup on all nodes
  hosts: all
  become: true
  roles:
    - common

- name: Install and start etcd
  hosts: etcd
  become: true
  roles:
    - etcd

- name: Generate CA and distribute certificates
  hosts: all
  become: true
  roles:
    - certificates

- name: Provision control-plane nodes
  hosts: controlplane
  become: true
  roles:
    - controlplane

- name: Provision worker nodes
  hosts: workers
  become: true
  roles:
    - worker
```

---

## 6. Ansible Roles (Detail)

### 6.1 Role: common

Installs Go, syncs the CCattler source, and builds the `cca` binary on every node.

```yaml
# ansible/roles/common/tasks/main.yml

---
- name: Install base packages
  apt:
    name:
      - curl
      - git
      - build-essential
      - containerd          # container runtime for worker nodes
    state: present
    update_cache: true

- name: Download Go {{ go_version }}
  get_url:
    url: "https://go.dev/dl/go{{ go_version }}.linux-amd64.tar.gz"
    dest: /tmp/go.tar.gz

- name: Extract Go to /usr/local
  unarchive:
    src: /tmp/go.tar.gz
    dest: /usr/local
    remote_src: true
    creates: /usr/local/go/bin/go

- name: Add Go to system PATH
  copy:
    dest: /etc/profile.d/go.sh
    content: |
      export PATH=$PATH:/usr/local/go/bin
      export GOPATH=/home/vagrant/go

- name: Sync CCattler source to VM
  synchronize:
    src: "{{ ccattler_src }}"
    dest: /opt/ccattler/
    rsync_opts:
      - "--exclude=.git"
      - "--exclude=.vagrant"

- name: Build CCattler binary
  shell: |
    export PATH=$PATH:/usr/local/go/bin
    cd /opt/ccattler
    go build -o /usr/local/bin/cca ./cmd/cca
  args:
    creates: /usr/local/bin/cca

- name: Verify cca binary
  command: /usr/local/bin/cca --help
  changed_when: false

- name: Create CCattler directories
  file:
    path: "{{ item }}"
    state: directory
    mode: "0755"
  loop:
    - /etc/ccattler
    - /etc/ccattler/pki
    - /var/lib/ccattler
    - /var/log/ccattler
```

### 6.2 Role: etcd

Installs etcd as a systemd service, listening on the node's private IP.

```yaml
# ansible/roles/etcd/tasks/main.yml

---
- name: Download etcd {{ etcd_version }}
  get_url:
    url: "https://github.com/etcd-io/etcd/releases/download/v{{ etcd_version }}/etcd-v{{ etcd_version }}-linux-amd64.tar.gz"
    dest: /tmp/etcd.tar.gz

- name: Extract etcd
  unarchive:
    src: /tmp/etcd.tar.gz
    dest: /tmp
    remote_src: true

- name: Install etcd binaries
  copy:
    src: "/tmp/etcd-v{{ etcd_version }}-linux-amd64/{{ item }}"
    dest: "/usr/local/bin/{{ item }}"
    mode: "0755"
    remote_src: true
  loop:
    - etcd
    - etcdctl

- name: Create etcd data directory
  file:
    path: /var/lib/etcd
    state: directory
    owner: root
    group: root
    mode: "0700"

- name: Install etcd systemd unit
  template:
    src: etcd.service.j2
    dest: /etc/systemd/system/etcd.service
  notify: restart etcd

- name: Enable and start etcd
  systemd:
    name: etcd
    enabled: true
    state: started
    daemon_reload: true

- name: Wait for etcd to be healthy
  command: >
    etcdctl --endpoints=http://{{ ansible_host }}:2379 endpoint health
  register: etcd_health
  retries: 10
  delay: 3
  until: etcd_health.rc == 0
  changed_when: false

  handlers:
    - name: restart etcd
      systemd:
        name: etcd
        state: restarted
```

```ini
# ansible/roles/etcd/templates/etcd.service.j2

[Unit]
Description=etcd — CCattler fact store
After=network.target

[Service]
Type=notify
ExecStart=/usr/local/bin/etcd \
  --name {{ inventory_hostname }} \
  --data-dir /var/lib/etcd \
  --listen-client-urls http://{{ ansible_host }}:2379,http://127.0.0.1:2379 \
  --advertise-client-urls http://{{ ansible_host }}:2379 \
  --listen-peer-urls http://{{ ansible_host }}:2380
Restart=on-failure
RestartSec=5

[Install]
WantedBy=multi-user.target
```

### 6.3 Role: certificates

Generates CCattler's internal CA hierarchy and distributes per-node certificates.
This maps to CCattler's zero-trust security model: mTLS everywhere, SPIFFE-style
identities, short-lived leaf certs.

```yaml
# ansible/roles/certificates/tasks/main.yml

---
# The CA is generated once on the first control-plane node, then distributed.
# In production the root CA would be offline; for local dev we generate it inline.

- name: Generate root CA (on first control-plane node only)
  when: inventory_hostname == groups['controlplane'][0]
  block:
    - name: Generate CA private key
      command: >
        openssl ecparam -genkey -name prime256v1
        -out {{ cca_cert_dir }}/ca-key.pem
      args:
        creates: "{{ cca_cert_dir }}/ca-key.pem"

    - name: Generate CA certificate
      command: >
        openssl req -new -x509
        -key {{ cca_cert_dir }}/ca-key.pem
        -out {{ cca_cert_dir }}/ca.pem
        -days 365
        -subj "/CN=CCattler Root CA/O=CCattler"
      args:
        creates: "{{ cca_cert_dir }}/ca.pem"

    - name: Read CA certificate
      slurp:
        src: "{{ cca_cert_dir }}/ca.pem"
      register: ca_cert_content

    - name: Read CA private key
      slurp:
        src: "{{ cca_cert_dir }}/ca-key.pem"
      register: ca_key_content

    - name: Store CA cert as fact
      set_fact:
        cca_ca_cert: "{{ ca_cert_content.content }}"
        cca_ca_key: "{{ ca_key_content.content }}"
      delegate_to: localhost
      delegate_facts: true

- name: Distribute CA certificate to all nodes
  copy:
    content: "{{ hostvars['localhost']['cca_ca_cert'] | b64decode }}"
    dest: "{{ cca_cert_dir }}/ca.pem"
    mode: "0644"
  when: inventory_hostname != groups['controlplane'][0]

- name: Generate node private key
  command: >
    openssl ecparam -genkey -name prime256v1
    -out {{ cca_cert_dir }}/node-key.pem
  args:
    creates: "{{ cca_cert_dir }}/node-key.pem"

- name: Set SPIFFE identity based on role
  set_fact:
    spiffe_id: >-
      {%- if 'controlplane' in group_names -%}
        spiffe://ccattler/controller/{{ inventory_hostname }}
      {%- elif 'workers' in group_names -%}
        spiffe://ccattler/node/{{ node_id | default(inventory_hostname) }}
      {%- elif 'etcd' in group_names -%}
        spiffe://ccattler/store/{{ inventory_hostname }}
      {%- endif -%}

- name: Generate CSR with SPIFFE SAN
  command: >
    openssl req -new
    -key {{ cca_cert_dir }}/node-key.pem
    -out {{ cca_cert_dir }}/node.csr
    -subj "/CN={{ inventory_hostname }}/O=CCattler"
    -addext "subjectAltName=URI:{{ spiffe_id }},IP:{{ ansible_host }}"
  args:
    creates: "{{ cca_cert_dir }}/node.csr"

- name: Sign node certificate (delegated to CA holder)
  # In a real setup, the CSR would be sent to the CA node.
  # For local dev, we copy the CA key temporarily to sign.
  block:
    - name: Temporarily place CA key for signing
      copy:
        content: "{{ hostvars['localhost']['cca_ca_key'] | b64decode }}"
        dest: "{{ cca_cert_dir }}/ca-key.pem"
        mode: "0600"
      when: inventory_hostname != groups['controlplane'][0]

    - name: Sign the CSR
      command: >
        openssl x509 -req
        -in {{ cca_cert_dir }}/node.csr
        -CA {{ cca_cert_dir }}/ca.pem
        -CAkey {{ cca_cert_dir }}/ca-key.pem
        -CAcreateserial
        -out {{ cca_cert_dir }}/node.pem
        -days 1
        -copy_extensions copyall
      args:
        creates: "{{ cca_cert_dir }}/node.pem"

    - name: Remove CA key from non-CA nodes
      file:
        path: "{{ cca_cert_dir }}/ca-key.pem"
        state: absent
      when: inventory_hostname != groups['controlplane'][0]

- name: Set certificate file permissions
  file:
    path: "{{ item }}"
    mode: "0600"
  loop:
    - "{{ cca_cert_dir }}/node-key.pem"
    - "{{ cca_cert_dir }}/node.pem"
```

### 6.4 Role: controlplane

Runs `cca server` as a systemd service, connecting to the shared etcd store.

```yaml
# ansible/roles/controlplane/tasks/main.yml

---
- name: Install cca-server systemd unit
  template:
    src: cca-server.service.j2
    dest: /etc/systemd/system/cca-server.service
  notify: restart cca-server

- name: Enable and start cca-server
  systemd:
    name: cca-server
    enabled: true
    state: started
    daemon_reload: true

- name: Wait for cca server API to respond
  uri:
    url: "http://{{ ansible_host }}:8443/state"
    method: GET
    status_code: [200, 401]     # 401 is fine — means the API is up, auth required
  register: api_check
  retries: 15
  delay: 3
  until: api_check.status is defined

  handlers:
    - name: restart cca-server
      systemd:
        name: cca-server
        state: restarted
```

```ini
# ansible/roles/controlplane/templates/cca-server.service.j2

[Unit]
Description=CCattler Server — controllers + API
After=network.target
Wants=network-online.target

[Service]
Type=simple
ExecStart=/usr/local/bin/cca server \
  --store etcd \
  --endpoints {{ etcd_endpoints }} \
  --cert {{ cca_cert_dir }}/node.pem \
  --key {{ cca_cert_dir }}/node-key.pem \
  --ca {{ cca_cert_dir }}/ca.pem
Restart=on-failure
RestartSec=5
StandardOutput=append:/var/log/ccattler/server.log
StandardError=append:/var/log/ccattler/server.log

[Install]
WantedBy=multi-user.target
```

With two control-plane nodes, CCattler's built-in leader election (Phase 13)
ensures only one actively runs controllers at a time, with automatic failover.

### 6.5 Role: worker

Runs `cca agent` as a systemd service with a unique node ID.

```yaml
# ansible/roles/worker/tasks/main.yml

---
- name: Configure containerd
  block:
    - name: Ensure containerd config directory exists
      file:
        path: /etc/containerd
        state: directory

    - name: Generate default containerd config
      shell: containerd config default > /etc/containerd/config.toml
      args:
        creates: /etc/containerd/config.toml

    - name: Enable and start containerd
      systemd:
        name: containerd
        enabled: true
        state: started

- name: Install cca-agent systemd unit
  template:
    src: cca-agent.service.j2
    dest: /etc/systemd/system/cca-agent.service
  notify: restart cca-agent

- name: Enable and start cca-agent
  systemd:
    name: cca-agent
    enabled: true
    state: started
    daemon_reload: true

- name: Wait for agent to register in etcd
  command: >
    etcdctl --endpoints={{ etcd_endpoints }}
    get /ccattler/node/{{ node_id }} --print-value-only
  register: node_registered
  retries: 10
  delay: 3
  until: node_registered.stdout | length > 0
  changed_when: false

  handlers:
    - name: restart cca-agent
      systemd:
        name: cca-agent
        state: restarted
```

```ini
# ansible/roles/worker/templates/cca-agent.service.j2

[Unit]
Description=CCattler Agent — node {{ node_id }}
After=network.target containerd.service
Requires=containerd.service

[Service]
Type=simple
ExecStart=/usr/local/bin/cca agent \
  --node-id {{ node_id }} \
  --store etcd \
  --endpoints {{ etcd_endpoints }} \
  --cert {{ cca_cert_dir }}/node.pem \
  --key {{ cca_cert_dir }}/node-key.pem \
  --ca {{ cca_cert_dir }}/ca.pem
Restart=on-failure
RestartSec=5
StandardOutput=append:/var/log/ccattler/agent.log
StandardError=append:/var/log/ccattler/agent.log

[Install]
WantedBy=multi-user.target
```

Each agent's `--node-id` matches the inventory variable, so the node registers
in the fact store as `node(worker-1)`, `node(worker-2)`, etc.

---

## 7. Bringing Up the Cluster

### 7.1 Start VMs and provision

```bash
cd /path/to/ccattler

# Start all VMs; Ansible runs automatically after the last worker boots
vagrant up

# Or provision separately if VMs are already running
vagrant up --no-provision
ansible-playbook -i ansible/inventory.ini ansible/site.yml
```

### 7.2 Verify connectivity

```bash
# SSH connectivity check
ansible all -i ansible/inventory.ini -m ping

# etcd health
vagrant ssh cca-etcd -c "etcdctl endpoint health"

# Check that cca server is running
vagrant ssh cca-ctrl-1 -c "systemctl status cca-server"

# Check that agents registered
vagrant ssh cca-ctrl-1 -c "cca get nodes --store etcd --endpoints http://192.168.56.10:2379"
```

Expected output from `cca get nodes`:

```
NODE        CPU    MEMORY    STATE
worker-1    2      2Gi       ready
worker-2    2      2Gi       ready
```

---

## 8. Testing: Deploy a Sample Workload

### 8.1 Sample CCL config

Create `test/cluster-test.ccl`:

```
service web {
    image nginx:1.27
    instances 4

    expose 8080

    health {
        http /health
        every 10s
    }

    resources {
        cpu 500m
        memory 256Mi
    }
}

service api {
    image my-api:latest
    instances 2

    expose 3000

    resources {
        cpu 250m
        memory 128Mi
    }
}
```

### 8.2 Apply and verify

```bash
# Submit the config to the shared etcd store
cca apply test/cluster-test.ccl --store etcd --endpoints http://192.168.56.10:2379

# Watch the fact store react in real time
cca watch --store etcd --endpoints http://192.168.56.10:2379

# Check service status — instances should spread across worker-1 and worker-2
cca get services --store etcd --endpoints http://192.168.56.10:2379
```

Expected output from `cca get services`:

```
SERVICE    DESIRED    RUNNING    IMAGE
web        4          4          nginx:1.27
api        2          2          my-api:latest
```

```bash
# Verify instances are distributed across nodes
cca get instances --store etcd --endpoints http://192.168.56.10:2379
```

Expected output:

```
INSTANCE       SERVICE    NODE        STATE
web-a1b2c3     web        worker-1    running
web-d4e5f6     web        worker-1    running
web-g7h8i9     web        worker-2    running
web-j0k1l2     web        worker-2    running
api-m3n4o5     api        worker-1    running
api-p6q7r8     api        worker-2    running
```

The scheduler's anti-affinity/spread rules should distribute instances
roughly evenly across available workers.

### 8.3 Failure recovery test

```bash
# Simulate a worker node failure — halt worker-2
vagrant halt cca-worker-2

# Wait for lease expiry (the agent's heartbeat stops, etcd lease TTL expires)
# The failure controller detects node_state(worker-2, unreachable) and triggers rescheduling.
sleep 30

# All instances should now be on worker-1
cca get instances --store etcd --endpoints http://192.168.56.10:2379

# Bring worker-2 back
vagrant up cca-worker-2
ansible-playbook -i ansible/inventory.ini ansible/site.yml --limit cca-worker-2

# The scheduler rebalances instances across both workers again
cca get instances --store etcd --endpoints http://192.168.56.10:2379
```

### 8.4 Scale test

```bash
# Scale web to 10 instances — should spread across both workers
cca scale web 10 --store etcd --endpoints http://192.168.56.10:2379

cca get instances --store etcd --endpoints http://192.168.56.10:2379
```

### 8.5 Cluster status overview

```bash
cca status --store etcd --endpoints http://192.168.56.10:2379
```

---

## 9. Operational Notes

### Rebuilding the binary after code changes

```bash
# Rebuild on all nodes without reprovisioning VMs
ansible all -i ansible/inventory.ini -m shell -a \
  "cd /opt/ccattler && export PATH=\$PATH:/usr/local/go/bin && go build -o /usr/local/bin/cca ./cmd/cca" \
  --become

# Restart services to pick up the new binary
ansible controlplane -i ansible/inventory.ini -m systemd -a "name=cca-server state=restarted" --become
ansible workers -i ansible/inventory.ini -m systemd -a "name=cca-agent state=restarted" --become
```

### Tearing down

```bash
vagrant destroy -f
```

### Logs

```bash
# Server logs
vagrant ssh cca-ctrl-1 -c "tail -f /var/log/ccattler/server.log"

# Agent logs
vagrant ssh cca-worker-1 -c "tail -f /var/log/ccattler/agent.log"

# etcd logs
vagrant ssh cca-etcd -c "journalctl -u etcd -f"
```

### Adding a third worker

1. Uncomment `cca-worker-3` in the inventory and set `NUM_WORKERS = 3` in the Vagrantfile.
2. Run `vagrant up cca-worker-3`.
3. Run `ansible-playbook -i ansible/inventory.ini ansible/site.yml --limit cca-worker-3`.
4. The new agent registers, and the scheduler begins placing instances on it.

### Adding a second control-plane node (HA)

1. Uncomment `cca-ctrl-2` in the inventory and set `NUM_CTRL = 2` in the Vagrantfile.
2. Run `vagrant up cca-ctrl-2`.
3. Run `ansible-playbook -i ansible/inventory.ini ansible/site.yml --limit cca-ctrl-2`.
4. Leader election ensures one server is active; the second takes over on failure.

---

## 10. Differences from the Kubernetes Approach

The blog post provisions Kubernetes with kubeadm, kubelet, and Flannel. CCattler
replaces all of these with its own components:

| Kubernetes concept | CCattler equivalent |
|--------------------|---------------------|
| kubeadm init | `cca server --store etcd --endpoints ...` |
| kubeadm join | `cca agent --node-id <id> --store etcd --endpoints ...` |
| kubelet | `cca agent` (observer + reconciler + reporter) |
| kube-apiserver | Built into `cca server` (the API component) |
| kube-scheduler | Built into `cca server` (scheduler controller) |
| kube-controller-manager | Built into `cca server` (all controllers) |
| Flannel CNI | CCattler's pluggable network provider |
| kubectl apply -f | `cca apply --store etcd` |
| YAML manifests | `.ccl` files (CCattler DSL) |
| Pods/Deployments/ReplicaSets | Facts: `service`, `instance`, `placement` |
| kubeadm PKI | CCattler's internal CA (ECDSA P-256, SPIFFE identities) |
| ServiceAccount tokens | Per-controller mTLS certificates with least privilege |

The key architectural difference: Kubernetes has many interacting REST APIs.
CCattler components communicate exclusively through the shared fact store (etcd).
There is no direct RPC between `cca server` and `cca agent` -- both read and
write facts, and the reconciliation loop drives convergence.
