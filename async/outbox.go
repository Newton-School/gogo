package async

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/Newton-School/gogo/core/db"
	"github.com/Newton-School/gogo/core/migrations"
	"github.com/Newton-School/gogo/core/models"
)

// OutboxMigration is installed explicitly when the Async outbox app is selected.
// Framework startup never creates tables or applies migrations automatically.
func OutboxMigration() migrations.Migration {
	schema := models.Schema{AppLabel: "gogo_async", Name: "Outbox", Table: "gogo_outbox", Fields: []models.Field{
		models.UUIDField("id", models.Primary), models.UUIDField("task_id", models.UniqueValue), models.CharField("task_name", models.WithMaxLength(192)), models.IntegerField("task_version"), models.JSONField("payload"), models.CharField("destination", models.WithMaxLength(192)), models.DateTimeField("available_at"), models.CharField("state", models.WithMaxLength(16)), models.IntegerField("attempts"), models.UUIDField("lease_token", models.Nullable), models.DateTimeField("lease_until", models.Nullable), models.DateTimeField("published_at", models.Nullable), models.CharField("last_error_code", models.WithMaxLength(64), models.Nullable),
	}, Indexes: []models.Index{{Name: "gogo_outbox_due", Fields: []string{"state", "available_at"}}}}
	return migrations.Migration{App: "gogo_async", Name: "0001_outbox", Operations: []migrations.Operation{migrations.CreateModel(schema)}}
}

type Outbox struct {
	Client      *Client
	Backend     db.Backend
	Lease       time.Duration
	BatchSize   int
	MaxAttempts int
}

// EnqueueOnCommit writes a durable intent on the caller's pinned transaction.
// The returned ID is not a broker acknowledgement and is discarded on rollback.
func (o *Outbox) EnqueueOnCommit(ctx context.Context, s Signature) (string, error) {
	if o.Client == nil || o.Backend == nil || !db.InTransaction(ctx, o.Backend.Alias()) {
		return "", errors.New("async: outbox requires an active business transaction")
	}
	e, err := o.Client.prepare(s)
	if err != nil {
		return "", err
	}
	if err := o.Client.authorize(ctx, "enqueue", e.Scope, e.ID); err != nil {
		return "", err
	}
	b, err := json.Marshal(e)
	if err != nil {
		return "", err
	}
	id := StableID(e.ID, "outbox")
	available := e.CreatedAt
	if e.ETA.After(available) {
		available = e.ETA
	}
	_, err = db.ExecutorFor(ctx, o.Backend).Exec(ctx, `INSERT INTO gogo_outbox (id,task_id,task_name,task_version,payload,destination,available_at,state,attempts) VALUES ($1,$2,$3,$4,$5,$6,$7,'pending',0)`, id, e.ID, e.Task, e.Version, b, e.Queue, available)
	if err != nil {
		return "", err
	}
	return e.ID, nil
}

func (o *Outbox) Tick(ctx context.Context) error {
	if o.Client == nil || o.Backend == nil {
		return ErrInvalid
	}
	if o.Backend.Dialect().Name() != "postgres" {
		return &db.Error{Code: db.UnsupportedFeature, Message: "Outbox requires a PostgreSQL adapter"}
	}
	if db.InTransaction(ctx, o.Backend.Alias()) {
		return errors.New("async: relay must run outside business transactions")
	}
	lease := o.Lease
	if lease == 0 {
		lease = 30 * time.Second
	}
	limit := o.BatchSize
	if limit == 0 {
		limit = 100
	}
	if lease <= 0 || limit < 1 || limit > 1000 || o.MaxAttempts < 0 {
		return ErrInvalid
	}
	token, err := NewID()
	if err != nil {
		return err
	}
	rows, err := o.Backend.Query(ctx, `UPDATE gogo_outbox SET state='leased',attempts=attempts+1,lease_token=$1,lease_until=CURRENT_TIMESTAMP + $2 * INTERVAL '1 millisecond' WHERE id IN (SELECT id FROM gogo_outbox WHERE state IN ('pending','leased') AND available_at<=CURRENT_TIMESTAMP AND (lease_until IS NULL OR lease_until<=CURRENT_TIMESTAMP) ORDER BY available_at,id LIMIT $3 FOR UPDATE SKIP LOCKED) RETURNING id,payload,attempts`, token, lease.Milliseconds(), limit)
	if err != nil {
		return err
	}
	type claimed struct {
		id       string
		payload  []byte
		attempts int
	}
	var items []claimed
	for rows.Next() {
		var item claimed
		if err := rows.Scan(&item.id, &item.payload, &item.attempts); err != nil {
			_ = rows.Close()
			return err
		}
		items = append(items, item)
	}
	err = rows.Err()
	closeErr := rows.Close()
	if err != nil {
		return err
	}
	if closeErr != nil {
		return closeErr
	}
	for _, item := range items {
		e, decodeErr := DecodeEnvelope(item.payload)
		var publishErr error
		var accepted Receipt
		if decodeErr == nil {
			accepted, publishErr = o.Client.publish(ctx, e)
		}
		if decodeErr == nil && publishErr == nil {
			result, err := o.Backend.Exec(ctx, `UPDATE gogo_outbox SET state='published',published_at=CURRENT_TIMESTAMP,lease_token=NULL,lease_until=NULL,last_error_code=NULL WHERE id=$1 AND lease_token=$2 AND lease_until>CURRENT_TIMESTAMP`, item.id, token)
			if err != nil {
				return err
			}
			n, err := result.RowsAffected()
			if err != nil {
				return err
			}
			if n != 1 {
				return ErrLeaseLost
			}
			o.Client.observeAccepted(ctx, e, accepted.State)
			continue
		}
		state := "pending"
		code := "publish_unconfirmed"
		if decodeErr != nil {
			state = "failed"
			code = "invalid_envelope"
		}
		if o.MaxAttempts > 0 && item.attempts >= o.MaxAttempts {
			state = "failed"
			code = "retries_exhausted"
		}
		delay := time.Second * time.Duration(1<<min(item.attempts, 9))
		result, err := o.Backend.Exec(ctx, `UPDATE gogo_outbox SET state=$3,available_at=CURRENT_TIMESTAMP + $4 * INTERVAL '1 millisecond',lease_token=NULL,lease_until=NULL,last_error_code=$5 WHERE id=$1 AND lease_token=$2 AND lease_until>CURRENT_TIMESTAMP`, item.id, token, state, delay.Milliseconds(), code)
		if err != nil {
			return err
		}
		n, err := result.RowsAffected()
		if err != nil {
			return err
		}
		if n != 1 {
			return ErrLeaseLost
		}
		if publishErr != nil {
			return publishErr
		}
		if decodeErr != nil {
			return ErrInvalid
		}
	}
	return nil
}
