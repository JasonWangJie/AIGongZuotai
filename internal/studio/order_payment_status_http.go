package studio

import (
	"context"
	"net/http"

	"github.com/jackc/pgx/v5"
)

func (a *App) orderPaymentStatusHTTP(w http.ResponseWriter, r *http.Request) {
	p := principalFrom(r)
	orderID := r.PathValue("order")
	var status OrderPaymentProjection
	err := a.orderIntentTx(p, func(ctx context.Context, tx pgx.Tx) error {
		if !validOperationsID(orderID, "ord") {
			return orderIntentMissing()
		}
		// A single statement reads the owner's frozen order, accepted payment
		// evidence and CREDIT result while the validated session remains locked.
		var err error
		status, err = orderPaymentProjectionSQL(ctx, tx, p.Workspace.ID, p.User.ID, orderID)
		return err
	})
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, status)
}
