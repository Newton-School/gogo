# Signals

Signals notify explicitly connected Go receivers about an in-process event. They are useful for local extension hooks; they are not a durable event bus.

## Typed receivers

Declare a typed signal and connect receivers with stable registration identities. Send a typed payload and a context. The package provides stop-on-error and robust delivery behavior; choose intentionally whether one receiver failure prevents later receivers from running.

Receiver order, duplicate registration, disconnect behavior and context cancellation are defined by the signal contract. Configure receivers before concurrent operation and keep callbacks prompt. Do not mutate shared payload state from unsynchronized goroutines.

## Database and distributed effects

A signal fired before commit can observe a change that later rolls back. Use the proper after-commit boundary for derived effects. A signal fired after commit can still be lost when the process exits before a receiver finishes.

Use [Async/outbox](scheduling.md) for durable cross-process work. Do not claim that an ORM hook or signal receiver makes external side effects exactly-once.

The technical signal guide and [services recipe](https://github.com/Newton-School/gogo/tree/master/examples/showcase/recipes/services) show typed stop/robust behavior.
