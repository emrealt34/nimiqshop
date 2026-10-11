package db

import "time"

// DeliveryTime uses the first fulfillment observation, never a later rating or
// email update. Historical quotes already retain the supplier's fulfillment time.
func (q Quote) DeliveryTime() time.Time {
	if q.DeliveredAt != nil && !q.DeliveredAt.IsZero() {
		return *q.DeliveredAt
	}
	if q.Status != "fulfilled" && q.Status != "delivered" && q.Status != "complete" {
		return time.Time{}
	}
	if !q.SupplierUpdatedAt.IsZero() {
		return q.SupplierUpdatedAt
	}
	if !q.GiftNotifiedAt.IsZero() {
		return q.GiftNotifiedAt
	}
	// An unrated historical row's last update is the best retained observation.
	if q.RatedAt == nil {
		return q.UpdatedAt
	}
	return time.Time{}
}

func (o Order) DeliveryTime() time.Time {
	if o.DeliveredAt != nil && !o.DeliveredAt.IsZero() {
		return *o.DeliveredAt
	}
	if isDeliveredStatus(o.Status) && o.RatedAt == nil {
		return o.UpdatedAt
	}
	return time.Time{}
}

// DeliverySeconds measures the recorded payment-confirmation to delivery window.
// Zero is a valid observation, not missing data. Creation/handoff is not payment.
func DeliverySeconds(created time.Time, paid *time.Time, delivered time.Time) (float64, bool) {
	if paid == nil || paid.IsZero() || delivered.IsZero() || paid.Before(created) || delivered.Before(*paid) || delivered.After(time.Now().UTC()) {
		return 0, false
	}
	return delivered.Sub(*paid).Seconds(), true
}
