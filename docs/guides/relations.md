# Relationships and deletion

Relations connect registered model schemas. The database relationship and the caller's permission to use it are separate decisions.

## Declare a foreign key

```go
models.ForeignKeyField("product", models.Relation{
    Target: "catalog.Product",
    OnDelete: models.Cascade,
}, models.WithStructField("ProductID"))
```

Store the target identity in a compatible Go field, such as `ProductID int64` for a big-auto target. Register both schemas before validation and migration planning.

## Relation options

| Option | Use |
| --- | --- |
| `Target`, `TargetFields` | Choose the target model and target key fields |
| `RelatedName` | Reverse instance accessor; a `+` suffix hides it |
| `RelatedQueryName` | Reverse traversal name in SQL queries |
| `Through`, `ThroughFields` | Explicit intermediary model and linking fields |
| `Symmetrical` | Self-many-to-many directionality; defaults to symmetric |
| `NoConstraint` | Explicit database-constraint choice, not permission to use invalid identities |

Default reverse accessors are based on the source model name. A one-to-one relation is unique. Many-to-many values are stored through a join table rather than as a source-model scalar column.

## Loading and changing relations

Use `SelectRelated` for supported to-one joins and prefetch operations for separately loaded relations. Relation managers expose explicit mutation methods. Review intermediary-model requirements, defaults, duplicates and transaction scope before changing a many-to-many relation.

Form, API and Admin relation inputs must resolve submitted IDs within the current caller's allowed scope. A valid integer ID or an existing database row does not establish authorization.

## Delete policies

| Policy | Intended behavior |
| --- | --- |
| `Cascade` | Collect dependent deletions |
| `Protect` | Refuse deletion while protected dependents exist |
| `Restrict` | Enforce restricted relation semantics in the collected graph |
| `SetNull` | Clear a nullable relation |
| `SetDefault` | Assign the declared relation default |
| `DoNothing` | Do not perform an ORM relation action; database constraints still apply |

Schema checks and backend constraints decide whether a particular combination is legal. Choosing `DoNothing` does not disable referential integrity.

## Preview is not authorization to execute

`DeleteCollector.Collect` is a preview. `Execute` recollects and locks the current scoped graph inside a transaction, authorizes it, runs read-only delete callbacks and applies its effects. Objects may have changed since preview.

Denied authorization, mutated callback views, failed SQL or failed commit cannot be reported as success. Hooks that add trusted database audit/business effects should use the same transaction executor. Do not keep record views and mutate them later from another goroutine.
