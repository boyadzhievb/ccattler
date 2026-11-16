# Spec Delta

## ADDED Requirements

### Requirement: Hierarchical services keep their counts
A declared service with a hierarchical name SHALL have its user intent, autoscaler intent, scale policy bounds and tenant quota resolved into its effective instance count exactly like a flat service.

#### Scenario: Count of payments/checkout reaches the effective count
- **GIVEN** a declared service `payments/checkout` with `intent/user/service/payments/checkout/instances = 3`
- **WHEN** the controllers reconcile
- **THEN** `effective/service/payments/checkout/instances` is 3, and its instances are created
- Test: `controllers/TestIntentResolverHierarchicalServiceCounts`, `integration/TestHierarchicalServiceScales`

#### Scenario: Flat and hierarchical services side by side
- **GIVEN** declared services `payments` (2 instances) and `payments/checkout` (3 instances)
- **WHEN** the controllers reconcile
- **THEN** each keeps its own count; neither is read as a field of the other
- Test: `controllers/TestIntentResolverHierarchicalServiceCounts`
