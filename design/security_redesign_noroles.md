You're right. I made a basic mistake: **you used LDAP as an example to illustrate why groups are a natural authorization mechanism. I turned that example into an integration requirement.** It has no place in this design brief.

And on the central question: I have not established any requirement for roles. Given the model you've described, there is no authorization capability that requires them. Groups can receive capabilities directly, and principals can belong to multiple groups. Roles add another layer without adding expressive power.

Here is the corrected brief for Claude. It contains no LDAP integration or assumptions.

# CCattler Authorization Redesign: Groups and Direct Capability Grants

## Objective

Redesign CCattler authorization around two mechanisms:

1. Groups collect principals.
2. Grants assign capabilities directly to principals or groups at specific scopes.

**Roles are not part of the target authorization model.**

Do not preserve roles as aliases, permission bundles, profiles, templates, or another intermediate authorization object. Remove the role abstraction rather than renaming it.

The goal is a minimal, explicit authorization model that makes it easy to determine who can perform an operation, where they can perform it, and why.

## 1. Authorization concepts

### Principals

Entities that can authenticate and perform operations:

- Users
- Services
- Nodes

### Groups

Groups are collections of principals.

- A principal can belong to multiple groups.
- A group can contain multiple principals.
- Groups can represent teams, responsibilities, or any deliberately selected collection of principals.
- A group can be created specifically to assign a particular permission to its members.
- Groups are not restricted to organizational departments or identity-management structures.

Group membership and permission assignment are independent.

### Capabilities

Capabilities represent authorized operations, for example:

- `workload.read`
- `workload.create`
- `workload.update`
- `node.read`
- `node.manage`
- `secret.write`

Retain CCattler's existing capability vocabulary and semantics unless the investigation identifies a concrete problem.

### Scopes

Scopes identify the resource boundary within which a capability applies.

Examples:

- `team/web`
- `team/finance`
- `cluster`

Retain existing scope hierarchy and matching semantics.

## 2. Direct capability grants

A grant assigns a capability to either a principal or a group at a particular scope.

Conceptually:

`Grant = Subject + Capability + Scope`

Subject is one of:

- User
- Service
- Node
- Group

Illustrative configuration:

```
group web-deployers {
    member alice
    member bob
}

group production-operators {
    member alice
}

grant workload.read, workload.create, workload.update
    to group web-deployers
    scope team/web

grant node.read, node.manage
    to group production-operators
    scope cluster

grant cluster.admin
    to user ops-admin
    scope cluster
```

The example illustrates desired semantics, not necessarily final parser grammar. Inspect existing DSL conventions and propose the most natural syntax.

A group can be created specifically to receive one or more capabilities. There is no requirement for a separate permission-bundle object.

## 3. Authorization evaluation

A principal's effective permissions are the union of:

1. Capabilities granted directly to that principal.
2. Capabilities granted to every group of which that principal is a member.

Example:

- Alice belongs to `web-deployers` and `production-operators`.
- `web-deployers` has `workload.create` in `team/web`.
- `production-operators` has `node.manage` in `cluster`.

Alice receives both permissions in their respective scopes.

Requirements:

- Direct principal grants must work independently of group membership.
- Group grants apply to all current members.
- Membership changes affect effective permissions.
- Grant changes affect effective permissions.
- Removing a membership removes permissions obtained through that membership.
- Removing a grant removes the corresponding permission.
- Group membership must not implicitly grant permissions.
- Group names must not carry implicit privileges.
- Preserve existing default-deny and scope enforcement semantics.

Effective permissions should be explainable: the system must be able to identify the exact grant and membership path responsible for an authorization decision.

## 4. Remove roles completely

Inspect and remove all persistent role concepts and role-dependent authorization paths.

Investigate:

- Role declarations in the DSL.
- Role facts and store keys.
- Role grants.
- Role capability and scope definitions.
- Built-in role definitions and fallback behavior.
- Role expansion in authorization controllers.
- CLI role commands.
- API endpoints and data structures exposing roles.
- Documentation and tests describing role-based authorization.

Do not retain roles under another name.

Do not introduce replacement concepts such as permission bundles, profiles, templates, or role-like abstractions.

The target authorization paths are:

```
Principal → Direct Capability Grants → Authorization Decision
```

and:

```
Principal → Group Membership → Group Capability Grants
          → Authorization Decision
```

These are the only permission-assignment paths.

## 5. CLI and operational management

The CLI should support:

- Granting capabilities to a user, service, node, or group.
- Revoking capabilities from a user, service, node, or group.
- Listing grants for a principal or group.
- Listing groups and their members.
- Inspecting a principal's effective permissions.
- Explaining why a principal has a particular permission, including its source grant and group membership.

Replace existing role-oriented CLI operations with direct capability grant/revoke operations following established CCattler CLI conventions.

## 6. Facts and ownership

Inspect the current fact model and ownership rules before proposing changes.

Define:

- Fact keys for direct capability grants.
- Fact keys for group membership, if existing ones are insufficient.
- Grant ownership and lifecycle.
- Membership ownership and lifecycle.
- Configuration reconciliation behavior.
- Deletion and recreation behavior for principals.
- Group deletion behavior.
- Authorization cache invalidation and consistency.

Do not change existing ownership semantics accidentally. Preserve them unless a concrete issue justifies a change.

## 7. Migration

Design a migration from the existing role-based facts and configuration to direct capability grants.

For every existing role assignment:

1. Resolve the capabilities and scopes represented by that role.
2. Determine the principal or group receiving the role.
3. Produce equivalent direct capability grants.
4. Verify that effective permissions remain unchanged.

Requirements:

- Preserve existing access wherever translation is unambiguous.
- Never silently widen privileges.
- Never silently discard permissions.
- Handle built-in role capabilities explicitly.
- Preserve scope restrictions.
- Detect and report ambiguous or unsupported cases.
- Make migration idempotent and auditable.
- Define backup and rollback procedures.
- Remove obsolete role facts only after successful verification.

Do not introduce a permanent compatibility role layer.

## 8. Security and correctness

Preserve existing CCattler security properties:

- Default-deny behavior.
- Exact capability matching.
- Scope restrictions and hierarchy.
- Tenant isolation.
- User, service and node authorization.
- Authentication and token validation.
- Audit logging.
- Secret authorization and redaction.
- HA consistency and authorization cache invalidation.

Analyze privilege changes caused by:

- Adding or removing group membership.
- Adding or removing grants.
- Deleting and recreating principals.
- Deleting groups.
- Configuration reconciliation.

Ensure revocation takes effect within the documented consistency guarantees.

## 9. Repository investigation

Before proposing implementation, inspect the actual repository.

Start with:

- `CLAUDE.md`
- `design/security-model.md`
- Relevant architecture and runtime invariant-flow documents.
- DSL parser and declaration types.
- Configuration compiler.
- Fact-key definitions.
- AuthController and authorization evaluation.
- User, group and grant management handlers.
- CLI authorization commands.
- Existing security and end-to-end tests.

Trace the complete lifecycle:

```
Configuration
    ↓
Facts
    ↓
Group Membership and Direct Grants
    ↓
Authorization Evaluation
    ↓
API / Controller Operation
```

Identify every current role dependency, including indirect dependencies.

Use implementation evidence rather than relying on documentation alone.

## 10. Deliverables

Produce a design proposal containing:

1. Current authorization architecture and role dependencies.
2. Proposed groups-and-direct-grants model.
3. Proposed fact model and key layout.
4. Proposed DSL grammar with examples.
5. Authorization evaluation algorithm.
6. CLI and API changes.
7. Ownership and lifecycle behavior.
8. Migration strategy and compatibility risks.
9. Security implications and failure scenarios.
10. Required unit, integration and end-to-end tests.
11. Affected design documents and invariant flows.

Include a before-and-after comparison of the authorization paths.

## 11. Constraints

- Design only. Do not implement yet.
- Do not modify repository files.
- Do not create branches, commits or pull requests.
- Do not push changes.
- Follow CCattler's existing architecture and invariants.
- Do not introduce Kubernetes-style object ontology.
- Do not add identity-provider integrations or unrelated infrastructure.
- Do not preserve roles for compatibility or familiarity.
- Do not introduce abstractions that duplicate groups and direct grants.

**The target is a complete authorization model based exclusively on principals, groups, capabilities, scopes and direct grants. Keep it that way.**

This is the model Claude should evaluate and design against—not a request to find a new justification for roles.