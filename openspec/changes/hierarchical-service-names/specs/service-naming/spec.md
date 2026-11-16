# Spec Delta

## Purpose

Defines which service names are valid, how a service fact key is split into its service name and field, what declares a service, and how names stored before the naming rule are reported and protected, so that one fact key always belongs to exactly one service.

## ADDED Requirements

### Requirement: Service names never contain a field root
A service name SHALL be refused when any segment after its first equals a reserved service field root. The first segment (the team or root name) is never checked. Field roots are the top-level attribute roots of service fact keys, so a name segment such as `cpu` or `path` that is only a nested field part stays valid.

#### Scenario: Valid hierarchical names
- **WHEN** a configuration declares `payments/checkout`, `frontend/web`, `payments/checkout/api`, `payments/cpu` and a flat `image`
- **THEN** it compiles and applies
- Test: `types/TestValidateServiceNameFieldRoots`, `lang/TestCompileRefusesServiceNamesWithFieldRoots`

#### Scenario: Name with a field root
- **WHEN** a configuration declares `web/image`, `web/instances` or `payments/checkout/image`
- **THEN** compile fails naming the service and the reserved segment, and no fact is written
- Test: `lang/TestCompileRefusesServiceNamesWithFieldRoots`, `api/TestApplyRefusesServiceNameWithFieldRoot`

### Requirement: Every write path checks the name
Every path that creates or changes a service SHALL apply the same name rule: configuration apply through the API (policy gate and direct), `cca apply --simulate`, `cca lint`, and the scale API.

#### Scenario: Lint and simulate
- **WHEN** `cca lint` or `cca apply --simulate` gets a configuration declaring `web/image`
- **THEN** both exit non-zero naming `web/image` and the field root `image`
- Test: `cmd/cca/TestLintAndSimulateRefuseServiceNameWithFieldRoot`

#### Scenario: Scale API
- **WHEN** a principal scales a name with a field root segment
- **THEN** the server answers 400 and writes nothing
- Test: `api/TestScaleRefusesServiceNameWithFieldRoot`

### Requirement: One fact key belongs to one service
A service fact key SHALL be split the same way by every reader: after the key prefix, the first segment belongs to the name; the first later segment that is a field root starts the field; without one, the key is the service's declaration marker. This SHALL hold for desired, effective, intent and derived service keys.

#### Scenario: Hierarchical name and nested field
- **WHEN** the keys `desired/service/payments/checkout/image`, `effective/service/payments/checkout/resources/cpu` and `desired/service/payments/checkout` are split
- **THEN** each belongs to service `payments/checkout`, with fields `image`, `resources/cpu` and none (marker)
- Test: `types/TestSplitServiceFactKeyRoundTrip`

#### Scenario: Overlapping names give the same answer every time
- **WHEN** services `payments` and `payments/checkout` are both declared and `desired/service/payments/checkout/image` is read by any controller, the scheduler, the status API or the tenant code
- **THEN** it is always `payments/checkout`'s image, never `payments`' field `checkout/image`
- Test: `types/TestSplitServiceFactKeyRoundTrip`, `controllers/TestServiceKeyReadersAgreeOnOverlappingNames`

#### Scenario: A new key without a registered root
- **WHEN** a service key constructor is added whose first field segment is not a registered field root
- **THEN** the build's tests fail naming the constructor
- Test: `types/TestEveryServiceKeyConstructorUsesARegisteredRoot`

### Requirement: Only a marker declares a service
A service SHALL count as declared only when its declaration marker `desired/service/{name}` exists with an empty value. Another service's field fact at the same path SHALL NOT declare it.

#### Scenario: A field fact is not a declaration
- **GIVEN** `desired/service/web` (marker) and `desired/service/web/image = "nginx:1.28"`, and an intent for `web/image`
- **WHEN** the controllers reconcile
- **THEN** `web/image` is treated as undeclared, and `web` keeps its image and count
- Test: `controllers/TestIntentResolverMarkerMustBeEmptyValued`

### Requirement: Stored invalid names are reported, not reinterpreted
When the store already holds a declaration whose name breaks the rule, `cca server` SHALL report at startup the invalid name, the colliding key and the valid service it may have damaged, and keep running. An apply that would write a key colliding with such a stored name SHALL be refused with the same details. Nothing SHALL silently move the invalid service's facts to another service.

#### Scenario: Startup report, either declaration order
- **GIVEN** a store where `web` and `web/image` were written in either order (with whichever value of `desired/service/web/image` that order left)
- **WHEN** `cca server` starts
- **THEN** its log names `web/image`, the key `desired/service/web/image` and the possibly damaged service `web`, and the server continues
- Test: `cmd/cca/TestServerReportsStoredInvalidServiceNames`

#### Scenario: Apply over a stored collision
- **GIVEN** a stored invalid service `web/image`
- **WHEN** a configuration declaring `web` with an image is applied
- **THEN** the apply is refused naming `web/image` and the key `desired/service/web/image`, and no fact changes
- Test: `api/TestApplyRefusedOverStoredNameCollision`
