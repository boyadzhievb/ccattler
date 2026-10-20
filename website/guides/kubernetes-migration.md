# Migrating from Kubernetes

This guide shows how to convert existing Kubernetes Deployment + Service YAML to CCattler DSL using the `k8s-to-cca` converter tool.

## Install the converter

The `k8s-to-cca` binary ships alongside `cca` in every release archive.

```bash
# Already in the release tarball:
tar xzf cca-v1.50.0-beta-linux-amd64.tar.gz
ls
# cca  k8s-to-cca

# Or build from source:
go build ./cmd/k8s-to-cca/
```

## Example: converting a web API deployment

Starting Kubernetes YAML (`web-api.yaml`):

```yaml
apiVersion: apps/v1
kind: Deployment
metadata:
  name: web-api
  labels:
    app: web-api
    team: payments
spec:
  replicas: 3
  selector:
    matchLabels:
      app: web-api
  strategy:
    type: RollingUpdate
    rollingUpdate:
      maxUnavailable: 1
      maxSurge: 1
  template:
    metadata:
      labels:
        app: web-api
    spec:
      containers:
        - name: web-api
          image: myregistry.io/web-api:v2.4.1
          ports:
            - containerPort: 8080
          resources:
            requests:
              cpu: "250m"
              memory: "128Mi"
            limits:
              cpu: "500m"
              memory: "256Mi"
          livenessProbe:
            httpGet:
              path: /healthz
              port: 8080
            initialDelaySeconds: 10
            periodSeconds: 15
          env:
            - name: DATABASE_URL
              valueFrom:
                secretKeyRef:
                  name: db-credentials
                  key: url
            - name: LOG_LEVEL
              value: "info"
---
apiVersion: v1
kind: Service
metadata:
  name: web-api
spec:
  type: ClusterIP
  selector:
    app: web-api
  ports:
    - port: 80
      targetPort: 8080
```

Run the converter:

```bash
k8s-to-cca web-api.yaml
```

Output (`web-api.cca`):

```hcl
service web-api {
    image "myregistry.io/web-api:v2.4.1"
    instances 3
    expose 8080
    expose 80
    health {
        http /healthz
        every 15s
    }
    resources {
        cpu 250m
        memory 128Mi
    }
    update {
        max_unavailable 1
        max_extra 1
    }
    config {
        env DATABASE_URL "secret:db-credentials/url"
        env LOG_LEVEL info
    }
    tags {
        team payments
    }
}
```

## What gets converted

| Kubernetes concept | CCattler equivalent |
|---|---|
| `kind: Deployment` | `service` block |
| `spec.replicas` | `instances` |
| `containers[0].image` | `image` |
| `containers[0].ports` | `expose` |
| `resources.requests` | `resources { cpu / memory }` |
| `livenessProbe.httpGet` | `health { http /path }` |
| `livenessProbe.tcpSocket` | `health { tcp }` |
| `strategy.rollingUpdate` | `update { max_unavailable / max_extra }` |
| `env[].value` | `config { env NAME value }` |
| `env[].valueFrom.secretKeyRef` | `config { env NAME "secret:name/key" }` |
| `metadata.labels` (non-standard) | `tags { key value }` |
| `kind: Service` ports | merged into `expose` |
| Secret volumes | `secret` block |

## What needs manual adjustment

The converter handles the most common patterns. Some Kubernetes features require manual translation:

**Secrets**: The converter shows `secret:name/key` as a placeholder in env vars. In CCattler, secrets are first-class:

```hcl
secret db-credentials {
    value "postgres://user:pass@host/db"
}

service web-api {
    # ...
    secret db-credentials
    config {
        env DATABASE_URL "/run/cca-secrets/db-credentials"
    }
}
```

**Connections**: For database connections, use the `connection` block instead of manual env vars:

```hcl
connection app-db {
    driver postgres
    host "db.internal"
    port 5432
    database "myapp"
    user "appuser"
    secret db-password
}

service web-api {
    connection app-db
}
```

**Placement constraints**: Kubernetes `nodeSelector` and `affinity` translate to CCattler `placement`:

```hcl
service web-api {
    placement {
        require gpu true
        prefer ssd true
        zone spread
    }
}
```

**Horizontal autoscaling**: Kubernetes HPA becomes an inline `scale` block:

```hcl
service web-api {
    scale {
        horizontal {
            min 2
            max 10
            target cpu 60
        }
    }
}
```

**Ingress / Gates**: Kubernetes Ingress or Gateway API translate to CCattler `gate`:

```hcl
gate web-public {
    host "api.example.com"
    service web-api
    port 8080
    sticky cookie {
        name __CCA_STICKY_API
    }
}
```

## Multi-document files

The converter handles multi-document YAML (separated by `---`). Deployments become `service` blocks; Services have their ports merged into the matching service's `expose` list. Other resource types (ConfigMap, ServiceAccount, Ingress) are skipped.

## What CCattler doesn't have

Some Kubernetes concepts have no direct equivalent because CCattler's architecture makes them unnecessary:

| Kubernetes | Why CCattler doesn't need it |
|---|---|
| Pod | Every service instance is a single container. Sidecars are separate services. |
| ReplicaSet | The instance controller manages replicas directly from `instances`. |
| Namespace | Tenants with hierarchical naming (`team/service`) replace namespaces. |
| ConfigMap | Inline `config` block or config files. No separate object. |
| PersistentVolumeClaim | `volume` block with `persistent true`. No claim/binding dance. |
| RBAC Role/ClusterRole | Capability-based permissions (`workload.read`, `secret.use`). |
| `apiVersion` / `kind` | Not needed. The DSL is typed by block structure, not metadata headers. |
