package transaction_test

import (
	"context"
	"net/netip"
	"time"

	"github.com/ellanetworks/ims/sip"
	"github.com/ellanetworks/ims/sip/transaction"
)

type registrar struct {
	layer *transaction.Layer
}

func (r *registrar) HandleRequest(tx *transaction.ServerTransaction, req *sip.Request) {
	err := r.layer.Go(func(ctx context.Context) {
		res := sip.NewResponse(req, 200, "")

		select {
		case <-time.After(10 * time.Millisecond):
		case <-ctx.Done():
			res = sip.NewResponse(req, 503, "")
		}

		_ = tx.Respond(res)
	})
	if err != nil {
		_ = tx.Respond(sip.NewResponse(req, 503, ""))
	}
}

func (*registrar) HandleCancel(*transaction.ServerTransaction, *sip.Request) {}

func (*registrar) HandleAck(*sip.Request) {}

func (*registrar) HandleTransactionError(*transaction.ServerTransaction, error) {}

func ExampleLayer_Go() {
	r := &registrar{}

	r.layer = transaction.New(transaction.Config{Handler: r})

	defer func() { _ = r.layer.Close() }()

	if _, err := r.layer.Listen(context.Background(), netip.MustParseAddrPort("127.0.0.1:5060")); err != nil {
		return
	}
}
