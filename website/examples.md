# Examples

Complete `.ccattler` configuration files demonstrating different capabilities. Each example can be run directly with the CLI.

## Basic service

The simplest possible deployment — one service, three instances.

```hcl
service web {
    image nginx:1.28
    instances 3
    expose 8080
    resources {
        cpu 500m
        memory 512Mi
    }
}
```

```bash
cca apply examples/basic.ccattler       # simulated
cca run examples/basic.ccattler          # real OS processes
cca run-container examples/basic.ccattler # Docker containers
```

## Multiple services

Two services with different resource profiles.

```hcl
service web {
    image nginx:1.28
    instances 4
    expose 8080
    resources {
        cpu 500m
        memory 512Mi
    }
}

service api {
    image myapp:latest
    instances 2
    expose 3000
    resources {
        cpu 250m
        memory 256Mi
    }
}
```

## Health checks

HTTP and TCP probes with configurable intervals.

```hcl
service web {
    image nginx:1.28
    instances 3
    expose 8080
    resources {
        cpu 500m
        memory 512Mi
    }
    health {
        http /health
        every 10s
    }
}

service database {
    image postgres:16
    instances 1
    expose 5432
    resources {
        cpu 1000m
        memory 2048Mi
    }
    health {
        tcp
        every 5s
    }
}
```

## Persistent storage

Volumes survive node failures and reattach to replacement instances.

```hcl
volume pgdata {
    size 50Gi
    persistent true
}

service postgres {
    image postgres:16
    instances 1
    expose 5432
    resources {
        cpu 1000m
        memory 2048Mi
    }
    volume pgdata /var/lib/postgresql/data
}

service web {
    image nginx:1.28
    instances 3
    expose 8080
    resources {
        cpu 500m
        memory 512Mi
    }
}
```

```bash
cca demo-storage   # simulated with node kill + volume migration
```

## Autoscaling with placement

Horizontal scaling, event-driven scaling, scheduled minimums, placement constraints, and rolling updates.

```hcl
service web {
    image nginx:1.28
    instances 3
    expose 8080

    resources {
        cpu 500m
        memory 512Mi
    }

    scale {
        horizontal {
            min 3
            max 30
            target cpu = 60
            target requests_per_second = 500
            event payments.pending = 20
            schedule {
                days weekdays
                start "08:00"
                end "18:00"
                minimum 10
            }
            stabilization {
                scale_up 60s
                scale_down 300s
            }
        }
    }

    placement {
        architecture amd64
        zone spread
    }

    update {
        max_unavailable 1
        max_extra 1
    }
}
```

## Container deployment with config

Real Docker containers with a mounted configuration file.

```hcl
service web {
    image nginx:1.28
    instances 2
    expose 80
    resources {
        cpu 500m
        memory 512Mi
    }
    config {
        file "/usr/share/nginx/html/index.html" "<h1>Hello from CCattler</h1>"
    }
}
```

```bash
cca run-container examples/web-container.ccattler
```

## Zabbix monitoring stack

Three-service Zabbix deployment with environment variable configuration and DNS-based service discovery.

```hcl
service postgres {
    image postgres:16
    instances 1
    expose 5432
    resources {
        cpu 500m
        memory 512Mi
    }
    config {
        env POSTGRES_DB "zabbix"
        env POSTGRES_USER "zabbix"
        env POSTGRES_PASSWORD "zabbix_pwd"
    }
}

service zabbix-server {
    image "zabbix/zabbix-server-pgsql:alpine-7.4-latest"
    instances 1
    expose 10051
    resources {
        cpu 500m
        memory 512Mi
    }
    config {
        env DB_SERVER_HOST "postgres"
        env POSTGRES_DB "zabbix"
        env POSTGRES_USER "zabbix"
        env POSTGRES_PASSWORD "zabbix_pwd"
    }
}

service zabbix-web {
    image "zabbix/zabbix-web-nginx-pgsql:alpine-7.4-latest"
    instances 1
    expose 8080
    resources {
        cpu 250m
        memory 256Mi
    }
    config {
        env ZBX_SERVER_HOST "zabbix-server"
        env DB_SERVER_HOST "postgres"
        env POSTGRES_DB "zabbix"
        env POSTGRES_USER "zabbix"
        env POSTGRES_PASSWORD "zabbix_pwd"
        env PHP_TZ "Europe/London"
    }
}
```

```bash
cca run-container examples/zabbix.ccattler
# Access Zabbix web UI at http://localhost:8080
# Default credentials: Admin / zabbix
```
