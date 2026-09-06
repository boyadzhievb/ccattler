Yes. Autoscaling actually fits very naturally into the fact/reconciliation model we've designed. I would not make "autoscaler" a special kind of workload. It is simply another controller that observes facts and writes desired state.

The key principle is:

Autoscalers don't perform scaling. They change desired capacity. The normal reconciliation system performs the change.

That gives us horizontal, vertical, and event-driven scaling without introducing separate resource/object concepts.

1. The basic model

Suppose we have:

service checkout

desired instances = 3

The scheduler observes:

desired instances = 3
running instances = 3

Everything is healthy.

Now metrics show:

CPU = 85%

The horizontal autoscaler evaluates its policy:

target CPU = 60%

and derives:

desired instances = 5

It doesn't create anything.

The state becomes:

desired instances = 5
running instances = 3

The ordinary reconciler sees:

desired != observed

and creates two more placements.

Eventually:

desired instances = 5
running instances = 5

The autoscaler is finished.

2. Horizontal Autoscaling

I'd express it like this:

service checkout {

    instances 3

    scale horizontally {

        min 2
        max 20

        cpu 60%

    }
}

Or, more explicitly:

autoscale checkout {

    dimension instances

    minimum 2
    maximum 20

    target cpu = 60%

}

The resulting facts could be:

desired_instances(checkout, 3)

autoscale_min(checkout, 2)
autoscale_max(checkout, 20)

autoscale_target(checkout, cpu, 60%)

The autoscaler observes:

cpu(checkout) = 87%

and derives:

autoscaler_recommendation(checkout, 5)

Then the policy engine commits:

desired_instances(checkout) = 5
3. Don't scale directly from instantaneous metrics

This is important.

If CPU goes:

60%
90%
55%
95%
50%

we don't want:

3 → 5 → 3 → 6 → 3

That's oscillation.

Instead the autoscaling controller should operate on time-series observations.

For example:

CPU average over 5 minutes = 78%

Then apply:

scale-up stabilization = 60s
scale-down stabilization = 5m

So:

high load
   ↓
observation window
   ↓
recommendation
   ↓
stabilization
   ↓
desired capacity

The state system records the recommendation and the policy that produced it.

4. Multiple metrics

This becomes particularly nice with our model.

autoscale checkout {

    min 2
    max 30

    target cpu = 60%
    target memory = 70%
    target requests_per_second = 500
}

The autoscaler calculates independently:

CPU recommendation       = 8
memory recommendation    = 5
request recommendation   = 12

Then:

desired instances = max(8, 5, 12)
                  = 12

This avoids having three independent systems fighting over the same desired state.

5. Vertical Autoscaling

Vertical scaling is slightly more interesting.

Instead of changing:

instances = 5

we change the resource requirements.

For example:

service checkout {

    resources {

        cpu 500m
        memory 1Gi

    }

    scale vertically {

        cpu {

            minimum 250m
            maximum 4
        }

        memory {

            minimum 512Mi
            maximum 8Gi
        }
    }
}

Observed usage:

CPU:
    requested = 500m
    actual    = 1.8 CPU

Memory:
    requested = 1Gi
    actual    = 3.2Gi

Vertical autoscaler derives:

recommended cpu = 2 CPU
recommended memory = 4Gi

Then updates desired resource requirements:

desired_cpu(checkout) = 2
desired_memory(checkout) = 4Gi

Again:

the autoscaler doesn't resize anything itself.

6. The tricky part: changing resources on a running process

Some Linux resource limits can be changed live.

Others require restarting/replacing the workload.

So our reconciler sees:

desired:

cpu = 2
memory = 4Gi

actual:

cpu = 500m
memory = 1Gi

and determines the required transition.

For example:

resize in place

or:

replace instance

This decision belongs to the runtime reconciler, not the autoscaler.

That's an important separation.

7. Horizontal + Vertical Together

We can allow both:

service checkout {

    scale {

        horizontal {

            min 2
            max 30

            cpu 60%
        }

        vertical {

            cpu {
                min 250m
                max 4
            }

            memory {
                min 512Mi
                max 8Gi
            }
        }
    }
}

Now the system has two independent dimensions:

                    capacity
                       │
             ┌─────────┴─────────┐
             │                   │
        number of            size of each
        instances             instance
             │                   │
        horizontal             vertical

But both ultimately modify desired state.

8. Event-Driven Autoscaling

This is where I think our architecture can become particularly powerful.

Suppose checkout processes messages from a queue.

Instead of:

CPU > 60%

we can say:

scale checkout based on:

queue payments.pending

Configuration:

service payment-worker {

    instances 2

    scale horizontally {

        min 1
        max 100

        event {

            source payments.pending

            target 20 messages / instance

        }
    }
}

Suppose:

queue depth = 800

Target:

20 messages / instance

Then:

800 / 20 = 40

Autoscaler derives:

desired instances = 40
9. Events Should Become Facts

This is another place where I'd avoid building an event-driven system around imperative commands.

Don't do:

QUEUE MESSAGE
    ↓
scale-up()

Instead:

queue_depth(payments.pending) = 800

That is an observation.

The autoscaler has a rule:

queue_depth / target_per_instance

which produces:

recommended_instances = 40

Then:

desired_instances = 40

The architecture remains:

OBSERVATION
     ↓
RULE
     ↓
DESIRED STATE
     ↓
RECONCILIATION
     ↓
REALITY

Exactly the same pattern as everything else.

10. Event-driven doesn't have to mean queues

We could support arbitrary signals.

For example:

scale on:

queue.depth
requests.rate
request.latency
cpu
memory
connections
custom.metric
external.signal

Or even combinations:

scale checkout {

    horizontal {

        min 2
        max 50

        when {

            requests_per_second > 1000
            AND latency_p95 > 200ms

        }
    }
}

Or:

queue_depth > 500
OR
cpu > 70%

The policy engine produces a recommendation.

11. Scheduled Autoscaling

We can also treat time as an observation.

For example:

service web {

    scale {

        horizontal {

            min 3
            max 50

            schedule {

                weekdays 08:00-18:00
                minimum 10

            }
        }
    }
}

This is particularly useful for predictable traffic.

At 08:00:

scheduled minimum = 10

The autoscaler doesn't need to "wake up and execute a command."

Time simply becomes another input:

current_time = 08:00

schedule says minimum = 10

→ desired_instances >= 10
12. Multiple Autoscaling Policies

Here's where we need to be careful.

Imagine:

CPU wants 8
queue wants 20
schedule wants 10

We should not have three controllers independently writing:

desired_instances = 8
desired_instances = 20
desired_instances = 10

That's a race.

Instead, autoscaling should have a single scaling decision function.

Conceptually:

recommendation = max(
    cpu_recommendation,
    queue_recommendation,
    scheduled_minimum
)

subject to:

min
max
quota
capacity
policy

Then only one component commits:

desired_instances = 20
13. Autoscaling and Quotas

This connects directly to our multi-tenancy design.

Suppose:

tenant quota = 100 instances

Autoscaler wants:

120

It cannot simply bypass the quota.

The policy engine evaluates:

requested = 120
tenant limit = 100

→ maximum allowed = 100

So:

desired = 100

But importantly, we should preserve the recommendation:

autoscaler wants = 120
desired = 100

That allows the UI/API to say:

Scaling is currently constrained by tenant quota.

That's much better than silently pretending the autoscaler wanted 100.

14. Cluster Capacity Is Another Constraint

Suppose:

desired = 100

but the cluster only has capacity for:

80

Then:

desired = 100
running = 80

The scheduler reports:

unsatisfied_capacity = 20

The autoscaler should not blindly keep increasing:

100 → 120 → 140 → 160

because that would create a runaway loop.

Instead the state should distinguish:

requested capacity
        ↓
allowed capacity
        ↓
schedulable capacity
        ↓
running capacity

For example:

autoscaler recommendation = 100

tenant quota              = 100

scheduler capacity        = 80

running                   = 80

The system knows exactly where the bottleneck is.

15. Cluster Autoscaling

Then we can take this one step further.

If workloads say:

desired = 100

but nodes can only accommodate:

80

the cluster autoscaler observes unsatisfied scheduling demand.

It can derive:

desired_nodes = 12

instead of:

desired_nodes = 10

Now we have hierarchical scaling:

Application autoscaler
        ↓
desired workload capacity
        ↓
Scheduler
        ↓
unsatisfied capacity
        ↓
Cluster autoscaler
        ↓
desired node capacity
        ↓
Infrastructure provider

Again, each level changes desired state.

16. The Beautiful Part: One Scaling Engine

I wouldn't implement three completely different systems.

I'd build one generic mechanism:

Scaling Engine

with three concepts:

Signal
Policy
Dimension

For example:

Signal:
    CPU = 82%

Policy:
    target = 60%

Dimension:
    instances

Or:

Signal:
    queue_depth = 800

Policy:
    20 messages / instance

Dimension:
    instances

Or:

Signal:
    memory = 3.2Gi

Policy:
    target = 70%

Dimension:
    memory

So:

signal
   ↓
scaling policy
   ↓
recommendation
   ↓
constraints
   ↓
desired state
17. The Complete Autoscaling Architecture
                 ┌────────────────────┐
                 │ Metrics / Events    │
                 │                    │
                 │ CPU                │
                 │ Memory             │
                 │ HTTP rate          │
                 │ Queue depth        │
                 │ Time               │
                 │ Custom signals     │
                 └─────────┬──────────┘
                           │
                           ▼
                 ┌────────────────────┐
                 │ Scaling Engine     │
                 │                    │
                 │ policies           │
                 │ prediction         │
                 │ stabilization      │
                 │ recommendations    │
                 └─────────┬──────────┘
                           │
                           ▼
                 ┌────────────────────┐
                 │ Policy / Limits    │
                 │                    │
                 │ tenant quota       │
                 │ min/max            │
                 │ security           │
                 │ cluster capacity   │
                 └─────────┬──────────┘
                           │
                           ▼
                 ┌────────────────────┐
                 │ Desired State      │
                 │                    │
                 │ instances = 40     │
                 │ cpu = 2            │
                 │ memory = 4Gi       │
                 └─────────┬──────────┘
                           │
                           ▼
                 ┌────────────────────┐
                 │ Reconciliation     │
                 └─────────┬──────────┘
                           │
                           ▼
                    Actual Runtime
18. And this gives us a very important invariant

I would make this an architectural rule:

Autoscalers may recommend and modify desired capacity; they may never directly manipulate runtime instances.

That means horizontal scaling, vertical scaling, event-driven scaling, scheduled scaling, and even cluster scaling all use the same underlying mechanism.

The entire system remains:

         FACTS
           ↓
       OBSERVATIONS
           ↓
        POLICIES
           ↓
    RECOMMENDATIONS
           ↓
      DESIRED STATE
           ↓
      RECONCILIATION
           ↓
         REALITY
           ↓
       OBSERVATIONS

And importantly, autoscaling itself doesn't need Kubernetes-style objects at all.

It's just another set of facts + policies + functions operating over the state graph.