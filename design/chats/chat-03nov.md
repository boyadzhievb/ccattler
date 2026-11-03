# Proposal: GitOps for CCattler

I would add Git as a first-class source of desired state in CCattler.

I would not integrate Argo CD itself, and I would not make CCattler consume Kubernetes `Application` resources. Argo CD's underlying pattern is valuable, though:

> Monitor a version-controlled source, turn its contents into desired state, compare that state with the current state, and reconcile the difference.

That fits CCattler particularly well because it already has a fact store, a declarative language, a compiler, and continuous controllers.

The key design decision is:

Git supplies desired-state inputs. CCattler remains responsible for policy, scheduling, execution, and observing reality.

## 1. The proposed architecture

Git repository

.cca files, .ccv values, environment configuration

Git source manager

Fetch, track revisions, detect changes

CCattler DSL pipeline

Resolve files → render values → parse → compile → validate

Source-owned desired facts

Calculate changes, enforce ownership, commit desired state

Existing CCattler controllers

Policies → effective state → placement → reconciliation

Node agents

Execute and report observations

Observed facts feed future reconciliation cycles

The Git integration ends at the desired-state boundary. It should not become another scheduler, runtime manager, or reconciliation engine.

## 2. What a Git source looks like

I would introduce a concept called a Source. A Source describes which part of a Git repository provides configuration for a CCattler environment.

For example, register one source:

```
cca source add payments-prod \
  --repo https://github.com/acme/platform-config.git \
  --revision refs/heads/main \
  --path environments/prod/payments \
  --values values/prod.ccv \
  --sync automatic \
  --prune
```

The repository might look like this:

```
platform-config/
├── environments/
│   ├── dev/
│   │   ├── payments/
│   │   │   ├── api.cca
│   │   │   ├── database.cca
│   │   │   └── values.ccv
│   │   └── frontend/
│   │       └── app.cca
│   │
│   └── prod/
│       └── payments/
│           ├── api.cca
│           ├── database.cca
│           └── values/
│               └── prod.ccv
```

The `.cca` files describe CCattler services and other desired configuration, not Kubernetes objects.

For example:

```
service payments/api {
    image "registry.example.com/payments-api:2.4.1"
    instances 4

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
```

The `.ccv` file supplies environment-specific values:

```
# prod.ccv
replicas: 4
image: registry.example.com/payments-api:2.4.1
```

The template can consume those values using the rendering functionality CCattler already has.

I would keep Source registration outside the application repository initially. That avoids a bootstrap problem where CCattler must first read a Git repository to discover the configuration telling it which Git repository to read.

A future declarative Source definition could be added once the lifecycle and ownership model are established.

## 3. The reconciliation lifecycle

A Git update would follow this sequence:

1. Detect a new revision. Poll the configured branch or receive a webhook notification. Resolve the branch to an exact commit SHA.
2. Fetch the repository. Check out the pinned commit and read only the configured path.
3. Build a candidate. Collect `.cca` files deterministically, load `.ccv` values, render templates, parse and compile the configuration.
4. Validate before changing anything. Check syntax, fact schemas, authorization, policies, resource ownership, and references. A parse or validation failure must leave the last good desired state intact.
5. Calculate the difference. Compare the new compiled facts with the facts previously owned by this Source.
6. Commit the changes. Update desired facts and remove eligible stale facts according to the Source's pruning policy.
7. Let existing controllers work. Scheduling, scaling, networking, storage, and agents proceed through the existing reconciliation model.
8. Report the outcome. Record both the Git revision and the observed health of the resulting workloads.

For example:

```
Commit a1b2c3
    |
    v
Source detects new revision
    |
    v
Fetch and compile configuration
    |
    v
Validate candidate
    |
    v
Apply desired-state changes
    |
    v
Controllers reconcile
    |
    v
Agents report observations
    |
    v
Source status + workload health
```

A webhook should eventually make updates faster, but I would start with polling. Polling is simpler to operate and gives CCattler a recovery mechanism even if webhook delivery fails. A webhook should only trigger a fresh fetch; it should never be trusted as the configuration itself.

## 4. The most important design problem: ownership and pruning

This is where I would spend the most design effort.

The current code already provides a good starting point:

- `cca apply` supports files and directories, remote etcd mode, values files, and template overrides ([command_apply.go](https://github.com/boyadzhievb/ccattler/blob/master/cmd/cca/command_apply.go?utm_source=chatgpt.com)).
- The command's rendering pipeline already handles `.cca` files and `.ccv` values ([helpers.go](https://github.com/boyadzhievb/ccattler/blob/master/cmd/cca/helpers.go?utm_source=chatgpt.com)).
- `lang.Compile` converts DSL declarations into facts ([compiler.go](https://github.com/boyadzhievb/ccattler/blob/master/lang/compiler.go?utm_source=chatgpt.com)).
- The store already exposes scans, revisions, watches, and transactions ([store.go](https://github.com/boyadzhievb/ccattler/blob/master/store/store.go?utm_source=chatgpt.com)).

However, there is an important gap: the current `ApplyWithBaseDir` writes compiled facts individually. It also handles removal of obsolete facts for gates declared in a file, but it does not provide general source-wide pruning of resources removed from a directory.

That is insufficient for reliable GitOps.

Imagine the repository previously contained:

```
service payments/api { ... }
service payments/worker { ... }
service payments/scheduler { ... }
```

A developer removes `scheduler` from Git.

CCattler needs to know that this resource was previously managed by this particular Git source. It must not simply delete every desired fact that is no longer present in the repository.

I propose a source ownership manifest:

```
source/payments-prod/
    config/...
    status/...
    ownership/
        <fact-key-hash> -> owned
```

On every successful candidate compilation, CCattler calculates:

```
previously owned facts
              |
              v
       compare with
              ^
              |
new compiled facts
```

Then:

- New facts are added.
- Changed facts are updated.
- Unchanged facts remain untouched.
- Previously owned facts missing from the new revision are deleted only when pruning is enabled.
- Facts owned by another source or another principal are never silently overwritten.

The ownership rule should be one authoritative writer per managed resource.

Manual `cca apply` and Git-managed configuration must not silently fight over the same resource.

There is also a transaction-size issue: the existing controller runner deliberately caps change sets to stay within etcd transaction limits. I would therefore make each resource's fact group atomic and make source application resumable and idempotent. The source must not report a revision as fully applied until all its changes have committed. Whole-application atomic publication could be a later improvement using versioned snapshots and an explicit active-generation switch; that would require adapting how controllers read desired state.

## 5. Source state and workload health must be separate

I would expose at least two independent statuses.

| Source synchronization | Workload health |
| ---------------------- | --------------- |
| Synced                 | Healthy         |
| OutOfSync              | Progressing     |
| Applying               | Degraded        |
| Failed                 | Unknown         |

For example:

```
SOURCE: payments-prod

Repository:       github.com/acme/platform-config
Branch:           main
Path:             environments/prod/payments

Fetched revision: a1b2c3d4
Applied revision: a1b2c3d4

Sync status:      SYNCED
Health status:    PROGRESSING

Last sync:        2026-11-03 10:42:00
```

`SYNCED` means the current Git revision has been successfully translated into desired facts.

`HEALTHY` means observations show that the system has reached the expected running condition.

Those are different statements. A repository can be fully synchronized while a workload is still starting or failing its health checks.

Also, a Git outage must not be interpreted as an empty repository. CCattler should retain the last successfully applied desired state and report the source as unavailable or failed. It must never prune resources merely because a fetch failed.

## 6. Security requirements

Git access introduces a new control-plane trust boundary.

I would require:

- Git credentials stored as CCattler-managed secrets or credential references, never embedded in repository URLs or committed files.
- A least-privileged service identity for each source.
- Scope restrictions, such as allowing `payments-prod` to manage only the `payments` tenant.
- Authorization and policy checks on all compiled facts. A Git commit does not automatically grant permission to modify cluster-wide configuration or authorization rules.
- Safe repository handling, including permitted URL protocols and hosts, path traversal protection, and symlink restrictions.
- No writes from the Git integration into `observed/`, `placement/`, or controller-owned `derived/` state.

The Git source should have permission to propose desired configuration, not impersonate node agents or controllers.

## 7. CLI experience

I would make the feature easy to inspect and operate:

```
cca source list
cca source add ...
cca source status payments-prod
cca source diff payments-prod
cca source sync payments-prod
cca source pause payments-prod
cca source resume payments-prod
cca source remove payments-prod
```

The commands above are proposed additions, not existing CLI functionality.

`cca source diff` would be especially valuable: it should show changes in CCattler concepts, not raw fact-store implementation details.

For example:

```
SOURCE: payments-prod
REVISION: a1b2c3d4

Changes:

  + service payments/api
      image: payments-api:2.4.0 -> payments-api:2.4.1

  ~ service payments/api
      instances: 3 -> 4

  - service payments/scheduler
      reason: removed from source

Pruning: enabled
```

## 8. Implementation roadmap

I would split implementation into three stages rather than build a complete Argo CD equivalent immediately.

1. Stage 1 — Git-backed configuration sources

   One repository, one revision, one path, `.cca` files, `.ccv` values, polling, compile/validate, source status, and source-owned fact tracking.

   No webhooks or sophisticated deployment strategies yet.
2. Stage 2 — Reliable source reconciliation

   General pruning, resource ownership enforcement, resumable application, concurrency protection, source-specific authorization, failure recovery, and integration tests.

   This stage is the correctness foundation. I would not enable automatic production pruning before it is complete.
3. Stage 3 — Production GitOps features

   Signed revision verification, webhook-triggered fetches, approval policies, previewing pull requests, revision pinning, rollback by reverting or repinning a Git revision, and multiple environments.

For high availability, the source polling and synchronization work should run under the existing control-plane leadership mechanism, not independently on every server replica.

## My recommendation

I would implement this as Git Source Management, with a first-class `Source` concept and a dedicated source reconciliation component.

I would deliberately avoid:

- Supporting Kubernetes manifests as the primary configuration format.
- Introducing a second reconciliation engine.
- Letting Git write observed state.
- Treating Git commit success as deployment success.
- Allowing one source to delete facts owned by another source.

This makes GitOps a natural extension of CCattler's architecture:

## The CCattler GitOps loop

# GIT → FACTS → POLICIES → DESIRED STATE

## RECONCILIATION → REALITY

## OBSERVATIONS → FACTS

The loop continues independently of Git availability.

The fundamental distinction: Git declares what the system should be. CCattler decides how to achieve it and verifies what actually happened.

I would make the first OpenSpec change proposal `git-backed-configuration-sources`, starting with source ownership, revision tracking, and safe synchronization semantics before implementing the Git polling machinery.