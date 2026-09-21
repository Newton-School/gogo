package examples_test

import (
	"context"
	"fmt"
	"testing"

	"github.com/Newton-School/gogo/core/auth"
	"github.com/Newton-School/gogo/core/models"
	"github.com/Newton-School/gogo/core/orm"
)

func Example_accountModels() {
	// docs:begin account-integer-ids
	identity := auth.AccountModels{} // 32-bit integers: 1, 2, 3, ...
	schemas := identity.Schemas()
	migrations := identity.Migrations()
	// docs:end account-integer-ids
	fmt.Println(schemas[0].PKFields()[0].Kind, len(migrations))
	// docs:begin account-long-ids
	identity, err := auth.NewAccountModels(models.BigAuto)
	if err != nil {
		panic(err) // Invalid startup configuration.
	}
	// docs:end account-long-ids
	fmt.Println(identity.User().Schema().PKFields()[0].Kind)
	// docs:begin account-uuid-ids
	identity, err = auth.NewAccountModels(models.UUID)
	if err != nil {
		panic(err)
	}
	// docs:end account-uuid-ids
	fmt.Println(identity.Group().Schema().PKFields()[0].Kind)
	// Output:
	// auto 1
	// big_auto
	// uuid
}

// Registration and migration execution happen before constructing the service.
// The caller supplies an authorizer based on the current trusted actor.
func accountService(store *orm.Store, identity auth.AccountModels, authorizeAccountChange func(context.Context, auth.AccountChange) error) (*auth.Accounts, error) {
	// docs:begin account-service-ids
	accounts, err := auth.NewAccounts(auth.AccountsConfig{
		Store:     store,
		Models:    identity, // Same choice as identity.Schemas() / .Migrations().
		Authorize: authorizeAccountChange,
	})
	// docs:end account-service-ids
	return accounts, err
}

func accountQuery(ctx context.Context, store *orm.Store, identity auth.AccountModels, userID string) (*auth.User, error) {
	// docs:begin account-query-ids
	user, err := orm.For(store, identity.User).
		Filter(orm.Q("id", userID)).Get(ctx)
	// docs:end account-query-ids
	return user, err
}

func TestAccountIDExamplesRequireStore(t *testing.T) {
	if _, err := accountService(nil, auth.AccountModels{}, nil); err == nil {
		t.Fatal("missing store accepted")
	}
}
