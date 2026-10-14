# Cloud Provider Integration

Cloud providers manage node lifecycle, load balancers, and VPC routes:

```
cloud {
    provider aws
    region "us-east-1"
    instance_type "m5.large"
    credentials infra_identity
    route_table "rtb-abc123"
}

service web {
    image nginx:1.27
    instances 3
    expose 8080
    expose external 443 http
}
```

The `cloud` block configures the provider. `expose external` marks a port for cloud load balancer creation. The cloud controller manager runs three sub-controllers:
- **NodeLifecycleController** — detects terminated cloud instances, disables and drains nodes
- **CloudLoadBalancerController** — creates/updates/deletes cloud LBs for `expose external` services
- **CloudRouteController** — programs VPC routes from node subnet assignments

Enabled via: `cca server --cloud-provider aws --cloud-region us-east-1`
