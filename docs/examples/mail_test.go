package examples_test

import (
	"context"
	"fmt"

	"github.com/Newton-School/gogo/core/mail"
)

func Example_mail() {
	// This bounded test outbox does not deliver email over the network.
	box, err := mail.NewMemory(10, 1<<20, mail.Limits{})
	if err != nil {
		panic(err)
	}
	receipt, err := box.Send(context.Background(), mail.Message{
		From: "shop@example.test", To: []string{"buyer@example.test"},
		Subject: "Order received", Text: "Thank you for your order.",
		HTML: "<p>Thank you for your order.</p>",
	})
	if err != nil {
		panic(err)
	}
	fmt.Println("simulated:", receipt.Simulated)
	fmt.Println("messages:", len(box.Outbox()))
	// Output:
	// simulated: true
	// messages: 1
}
