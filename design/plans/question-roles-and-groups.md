# Question: roles, groups and direct grants in CCattler

A design question to discuss with someone outside the project. It is about how
permissions are handed out, not how they are checked.

## Background

CCattler is a container orchestrator, an alternative to Kubernetes. Its authorization model:

- **Principals**: users, services and nodes.
- **Groups**: an attribute of a principal, set in the configuration or supplied by the identity provider (OIDC).
- **Capabilities**: fine-grained permissions such as `workload.read`, `workload.create`, `secret.use` and `cluster.admin`.
- **Scopes**: hierarchical paths such as `cluster`, `team/payments` and `node/node01`.
- **A grant** is a capability at a scope, given to a principal or to a group. The authorizer checks:
  does the principal hold capability X at scope S or above?

The intended model is "Unix-style": groups plus direct capability grants. A role is only an optional
alias for a bundle of capabilities. There are no role hierarchies, and Kubernetes-style
Role/ClusterRole/RoleBinding is deliberately avoided.

## What exists today

There are three ways to give permissions, and they don't match:

| Path | Syntax | Direct capability? |
|---|---|---|
| CLI | `cca role grant user:alice --capability workload.read --scope team/web` | yes |
| CLI | `cca role grant user:alice --role api-reader` | no, built-in bundle |
| DSL (config file) | `role developer { allow service.read … }` then `grant developer to group developers` | **no**, a named role is required |

Built-in roles: `cluster-admin`, `api-reader` (five read capabilities) and `api-writer`.

## Problems found in review

1. **No direct grants in the DSL.** The DSL can only grant a named role. So the configuration
   cannot express the "groups plus direct capabilities" model that the documentation describes.
   The security model doc even shows `grant workload.read, workload.update to group developers scope team/web`,
   but that syntax does not exist.
2. **Roles are live aliases, not snapshots.** A built-in role is expanded every time grants are
   rebuilt, not when the grant is made. Changing the bundle in a new release silently changes what
   every existing holder can do.
3. **Built-in role names can be overridden.** A user-defined role with the same name as a built-in
   role replaces it. So `api-reader` can mean different things on different clusters.
4. **Group membership on user delete.** Deleting a user removes their grants and tokens. Group
   memberships written in the configuration are kept, so re-creating the same name re-joins those
   groups. That is a deliberate decision, but debatable.

## Questions

1. Should the DSL support direct grants, for example
   `grant workload.read, workload.update to group deployers scope team/web`?
   Should that become the main form, with roles as optional shorthand, or should configurations
   always go through named roles?
2. Should a role expand when it is granted (a snapshot: stable, but upgrades don't propagate) or at
   evaluation time (a live alias: one change updates everyone, which can also widen access silently)?
3. Should built-in role names be reserved, so a user-defined role cannot override them?
4. Where should group membership live: in the config file, in the identity provider (OIDC group
   claims), or both? If both, which one wins?
5. Is "Unix-style" the right model at all? How do these systems compare for an orchestrator?

| System | Model |
|---|---|
| Unix | users belong to groups, and permissions are set directly per group |
| Kubernetes | named Roles are bound to subjects through RoleBindings; nothing is granted without a role |
| Vault | named policies are attached to entities and groups; nothing is granted without a policy |
| AWS IAM | policies are attached to users, groups or roles; inline policies are the direct form |

## Current leaning

- Add direct grants to the DSL and make them the documented main form.
- Keep roles as optional aliases.
- Reserve built-in role names.
- Expand roles at evaluation time, but document it.
