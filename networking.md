Exactly. And this is another place where I think CCattler should keep the abstraction but change the architecture.

Kubernetes has the CNI (Container Network Interface) as the interface between Kubernetes/container runtimes and the actual networking implementation. Cilium, Calico, Flannel, etc. implement different parts of that ecosystem.

For CCattler, I'd do something similar—but make networking a first-class pluggable subsystem, rather than baking assumptions about one network technology into the control plane.

1. CCattler should define a Network Provider interface

Conceptually:

CCattler Network Controller
          │
          ▼
     Network API
          │
    ┌─────┼─────────┐
    ▼     ▼         ▼
 Cilium  Calico   Simple-Linux

The CCattler control plane understands:

identities
endpoints
networks
connectivity policies
service discovery
routes
encryption requirements

It doesn't need to know how packets physically get there.

2. Define networking in terms of intent

Instead of:

use Cilium

the user says:

network payments {

    allow checkout -> database:5432
    deny frontend -> database:5432

    encryption required
}

CCattler produces facts such as:

network(payments)

endpoint(checkout)
endpoint(database)

allow(checkout, database, 5432)
deny(frontend, database, 5432)

encryption_required(payments)

The network provider turns those facts into actual networking.

3. The provider gets a clean contract

Something like:

NetworkProvider

create_network()
delete_network()

attach_endpoint()
detach_endpoint()

publish_endpoint()

apply_policy()

observe()


But I'd actually go one level higher than Kubernetes CNI.

CNI is primarily concerned with connecting a container to a network.

CCattler needs a broader abstraction:

NetworkProvider
│
├── connectivity
├── addressing
├── routing
├── service discovery
├── policy enforcement
├── encryption
└── observability

Then a provider can implement these using different technologies.

4. Flannel vs Calico vs Cilium

The important thing is that they're not really equivalent products.

Very roughly:

Technology	Main strength
Flannel	Simple pod networking/overlay
Calico	Networking + network policy
Cilium	eBPF networking + policy + observability

So CCattler shouldn't define:

CNI = networking

as the entire abstraction.

Instead:

                    CCattler Network API
                            │
              ┌─────────────┼─────────────┐
              ▼             ▼             ▼
          Overlay         Routing       Policy
              │             │             │
              └─────────────┼─────────────┘
                            ▼
                    Network Provider
5. And we can make the provider replaceable

For laptop development:

SimpleProvider

could literally use Linux networking.

For example:

CCattler
   ↓
SimpleProvider
   ↓
Linux network namespace
   ↓
veth
   ↓
bridge
   ↓
host

Then later:

CCattler
   ↓
CiliumProvider
   ↓
eBPF

or:

CCattler
   ↓
CalicoProvider
   ↓
Calico

The CCattler networking model doesn't change.

6. This also fits our identity model

This is where I think CCattler can become cleaner than Kubernetes.

We already have identities:

spiffe://ccattler/frontend/web
spiffe://ccattler/payments/checkout
spiffe://ccattler/payments/database

So network policy can be identity-based:

allow:

frontend/web
    →
payments/checkout
    port 443

and:

deny:

frontend/web
    →
payments/database

The network provider translates that into whatever mechanism it supports:

eBPF
iptables/nftables
VXLAN
WireGuard
routing tables
cloud security groups

The user never needs to know.

7. CNI should be an implementation detail

I'd therefore structure CCattler roughly like:

                 CCattler
                    │
             Network Controller
                    │
              Network Intent
                    │
        ┌───────────┴───────────┐
        │                       │
  Endpoint model           Policy model
        │                       │
        └───────────┬───────────┘
                    │
             Network Provider
                    │
       ┌────────────┼─────────────┐
       ▼            ▼             ▼
    Linux        Cilium        Calico

And there could be a fourth:

CCattler Local Network Provider

specifically designed for the laptop development environment.

That means we can test 90% of CCattler's networking semantics without requiring a real Kubernetes cluster or Cilium installation.

One more important distinction

I wouldn't force CCattler to use CNI itself.

I'd probably support CNI/OCI-compatible runtimes at the infrastructure boundary where useful, but make CCattler Network Provider its own higher-level abstraction.

Because our goal isn't:

"How do we build another Kubernetes CNI?"

It's:

"How should a container management system express and reconcile networking independently of the underlying packet implementation?"

That distinction will matter a lot once we get into service discovery, ingress, DNS, encryption, multi-host networking, network policy, and eBPF.