package queue_test

import (
	"errors"
	"testing"

	"github.com/Newton-School/gogo/queue"
	_ "github.com/Newton-School/gogo/queue/backends"
	_ "github.com/Newton-School/gogo/queue/backends/redis"
	_ "github.com/Newton-School/gogo/queue/brokers"
	_ "github.com/Newton-School/gogo/queue/brokers/redis"
	_ "github.com/Newton-School/gogo/queue/schedulers/redis"
)

func TestRuntimeFactoriesCreateMemoryImplementations(t *testing.T) {
	broker, err := queue.NewBrokerFromURL(queue.RuntimeConfig{BrokerURL: "memory://"})
	if err != nil {
		t.Fatalf("NewBrokerFromURL(memory) error = %v", err)
	}
	if broker == nil {
		t.Fatal("NewBrokerFromURL(memory) returned nil broker")
	}

	backend, err := queue.NewResultBackendFromURL(queue.RuntimeConfig{ResultBackend: "memory"})
	if err != nil {
		t.Fatalf("NewResultBackendFromURL(memory) error = %v", err)
	}
	if backend == nil {
		t.Fatal("NewResultBackendFromURL(memory) returned nil backend")
	}

	store, err := queue.NewScheduleStoreFromURL(queue.RuntimeConfig{ScheduleStore: "memory://"})
	if err != nil {
		t.Fatalf("NewScheduleStoreFromURL(memory) error = %v", err)
	}
	if store == nil {
		t.Fatal("NewScheduleStoreFromURL(memory) returned nil store")
	}
}

func TestRuntimeFactoriesCreateRedisImplementations(t *testing.T) {
	for _, rawURL := range []string{"redis://localhost:6379/0", "rediss://localhost:6379/1"} {
		broker, err := queue.NewBrokerFromURL(queue.RuntimeConfig{BrokerURL: rawURL})
		if err != nil {
			t.Fatalf("NewBrokerFromURL(%s) error = %v", rawURL, err)
		}
		if broker == nil {
			t.Fatalf("NewBrokerFromURL(%s) returned nil broker", rawURL)
		}

		backend, err := queue.NewResultBackendFromURL(queue.RuntimeConfig{ResultBackend: rawURL})
		if err != nil {
			t.Fatalf("NewResultBackendFromURL(%s) error = %v", rawURL, err)
		}
		if backend == nil {
			t.Fatalf("NewResultBackendFromURL(%s) returned nil backend", rawURL)
		}

		store, err := queue.NewScheduleStoreFromURL(queue.RuntimeConfig{ScheduleStore: rawURL})
		if err != nil {
			t.Fatalf("NewScheduleStoreFromURL(%s) error = %v", rawURL, err)
		}
		if store == nil {
			t.Fatalf("NewScheduleStoreFromURL(%s) returned nil store", rawURL)
		}
	}
}

func TestRuntimeFactoriesRejectUnsupportedProductionURLs(t *testing.T) {
	if _, err := queue.NewBrokerFromURL(queue.RuntimeConfig{BrokerURL: "amqp://localhost:5672/"}); !errors.Is(err, queue.ErrUnsupportedRuntimeURL) {
		t.Fatalf("NewBrokerFromURL(amqp) error = %v, want ErrUnsupportedRuntimeURL", err)
	}
	if _, err := queue.NewResultBackendFromURL(queue.RuntimeConfig{ResultBackend: "amqp://localhost:5672/"}); !errors.Is(err, queue.ErrUnsupportedRuntimeURL) {
		t.Fatalf("NewResultBackendFromURL(amqp) error = %v, want ErrUnsupportedRuntimeURL", err)
	}
	if _, err := queue.NewScheduleStoreFromURL(queue.RuntimeConfig{ScheduleStore: "amqp://localhost:5672/"}); !errors.Is(err, queue.ErrUnsupportedRuntimeURL) {
		t.Fatalf("NewScheduleStoreFromURL(amqp) error = %v, want ErrUnsupportedRuntimeURL", err)
	}
}

func TestRuntimeFactoriesRejectMalformedRedisURLs(t *testing.T) {
	if _, err := queue.NewBrokerFromURL(queue.RuntimeConfig{BrokerURL: "redis://localhost/not-a-db"}); !errors.Is(err, queue.ErrUnsupportedRuntimeURL) {
		t.Fatalf("NewBrokerFromURL(malformed redis) error = %v, want ErrUnsupportedRuntimeURL", err)
	}
	if _, err := queue.NewResultBackendFromURL(queue.RuntimeConfig{ResultBackend: "redis://localhost/not-a-db"}); !errors.Is(err, queue.ErrUnsupportedRuntimeURL) {
		t.Fatalf("NewResultBackendFromURL(malformed redis) error = %v, want ErrUnsupportedRuntimeURL", err)
	}
	if _, err := queue.NewScheduleStoreFromURL(queue.RuntimeConfig{ScheduleStore: "redis://localhost/not-a-db"}); !errors.Is(err, queue.ErrUnsupportedRuntimeURL) {
		t.Fatalf("NewScheduleStoreFromURL(malformed redis) error = %v, want ErrUnsupportedRuntimeURL", err)
	}
}
