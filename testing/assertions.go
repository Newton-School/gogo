package testing

import (
	"encoding/json"
	"fmt"
	"reflect"

	"github.com/Newton-School/gogo/auth"
	"github.com/Newton-School/gogo/forms"
	"github.com/Newton-School/gogo/signals"
)

type QueryCounter struct {
	count int
}

type QueryCount interface {
	Count() int
}

func NewQueryCounter() *QueryCounter {
	return &QueryCounter{}
}

func (c *QueryCounter) Record() {
	c.count++
}

func (c *QueryCounter) Count() int {
	if c == nil {
		return 0
	}
	return c.count
}

func AssertEqualJSON(t TestHelper, actual any, expected any) {
	t.Helper()
	actualValue, err := normalizeJSON(actual)
	if err != nil {
		t.Fatalf("actual JSON is invalid: %v", err)
	}
	expectedValue, err := normalizeJSON(expected)
	if err != nil {
		t.Fatalf("expected JSON is invalid: %v", err)
	}
	if !reflect.DeepEqual(actualValue, expectedValue) {
		t.Fatalf("JSON mismatch: actual=%#v expected=%#v", actualValue, expectedValue)
	}
}

func AssertHasFieldError(t TestHelper, errors any, field string, message string) {
	t.Helper()
	messages := fieldErrorMessages(errors, field)
	for _, candidate := range messages {
		if candidate == message {
			return
		}
	}
	t.Fatalf("field %s errors = %#v, want %q", field, messages, message)
}

func AssertHasNonFieldError(t TestHelper, errors any, message string) {
	t.Helper()
	messages := nonFieldErrorMessages(errors)
	for _, candidate := range messages {
		if candidate == message {
			return
		}
	}
	t.Fatalf("non-field errors = %#v, want %q", messages, message)
}

func AssertQueryCount(t TestHelper, counter QueryCount, expected int) {
	t.Helper()
	if counter == nil {
		t.Fatalf("query counter is nil, want %d", expected)
	}
	if got := counter.Count(); got != expected {
		t.Fatalf("query count = %d, want %d", got, expected)
	}
}

func AssertSignalSent(t TestHelper, responses []signals.Response) {
	t.Helper()
	if len(responses) == 0 {
		t.Fatalf("signal was not sent to any receiver")
	}
	for _, response := range responses {
		if response.Error != nil {
			t.Fatalf("signal receiver %d returned error: %v", response.ReceiverID, response.Error)
		}
	}
}

func AssertEmailSent(t TestHelper, outbox *MailOutbox, subject string) {
	t.Helper()
	if outbox == nil {
		t.Fatalf("mail outbox is nil")
	}
	outbox.AssertEmailSent(t, subject)
}

func AssertTaskEnqueued(t TestHelper, harness *QueueHarness, name string) {
	t.Helper()
	if harness == nil {
		t.Fatalf("queue harness is nil")
	}
	harness.AssertTaskEnqueued(t, name)
}

func AssertPermissionGranted(t TestHelper, user auth.User, permission string) {
	t.Helper()
	if !auth.HasPerm(user, permission) {
		t.Fatalf("permission %s was denied for user %#v", permission, user)
	}
}

func AssertPermissionDenied(t TestHelper, user auth.User, permission string) {
	t.Helper()
	if auth.HasPerm(user, permission) {
		t.Fatalf("permission %s was granted for user %#v", permission, user)
	}
}

func normalizeJSON(value any) (any, error) {
	var data []byte
	switch typed := value.(type) {
	case string:
		data = []byte(typed)
	case []byte:
		data = typed
	case *Response:
		if typed == nil {
			return nil, fmt.Errorf("nil response")
		}
		data = []byte(typed.Body)
	default:
		encoded, err := json.Marshal(value)
		if err != nil {
			return nil, err
		}
		data = encoded
	}
	var decoded any
	if err := json.Unmarshal(data, &decoded); err != nil {
		return nil, err
	}
	return decoded, nil
}

func fieldErrorMessages(value any, field string) []string {
	switch errors := value.(type) {
	case forms.ErrorDict:
		return errors.Get(field).Messages()
	case map[string][]string:
		return append([]string(nil), errors[field]...)
	case map[string]forms.ErrorList:
		return errors[field].Messages()
	case *Response:
		if errors == nil {
			return nil
		}
		var parsed map[string][]string
		if err := json.Unmarshal([]byte(errors.Header.Get(formErrorsHeader)), &parsed); err != nil {
			return nil
		}
		return append([]string(nil), parsed[field]...)
	default:
		return nil
	}
}

func nonFieldErrorMessages(value any) []string {
	switch errors := value.(type) {
	case forms.NonFieldErrorList:
		return errors.Messages()
	case forms.ErrorList:
		return errors.Messages()
	case []string:
		return append([]string(nil), errors...)
	case map[string][]string:
		return append([]string(nil), errors["__all__"]...)
	case *Response:
		return fieldErrorMessages(errors, "__all__")
	default:
		return nil
	}
}
