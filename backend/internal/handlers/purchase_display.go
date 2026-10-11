package handlers

import (
	"encoding/json"
	"strconv"
	"strings"
	"time"

	"nimiqshop/internal/activityvisibility"
	"nimiqshop/internal/db"
)

func orderFaceDetail(o db.Order) string {
	var payload map[string]interface{}
	_ = json.Unmarshal(o.Payload, &payload)
	var value float64
	switch v := payload["value"].(type) {
	case float64:
		value = v
	case string:
		value, _ = strconv.ParseFloat(v, 64)
	}
	return giftFaceDetail(db.QuoteLine{FaceLabel: payloadField(o.Payload, "face_label"),
		Denomination: payloadField(o.Payload, "denomination"), ProductValue: value,
		ProductCurrency: payloadField(o.Payload, "currency"), Country: payloadField(o.Payload, "country")})
}

func deliveryTimePointer(t time.Time) *time.Time {
	if t.IsZero() {
		return nil
	}
	return &t
}

func quoteFaceSummary(q db.Quote) string {
	var parts []string
	for _, line := range giftLinesFor(q) {
		label := giftFaceDetail(line)
		if label == "" {
			continue
		}
		if line.Quantity > 1 {
			label += " ×" + strconv.Itoa(line.Quantity)
		}
		parts = append(parts, label)
	}
	return strings.Join(parts, " + ")
}

func recordedDeliverySummary(orders []db.Order, quotes []db.Quote) map[string]interface{} {
	var total float64
	count := 0
	add := func(created time.Time, paid *time.Time, delivered time.Time) {
		if seconds, ok := db.DeliverySeconds(created, paid, delivered); ok {
			total += seconds
			count++
		}
	}
	for _, o := range orders {
		if activityvisibility.Visible(o.ID, false) && (o.Status == "delivered" || o.Status == "fulfilled" || o.Status == "complete") {
			add(o.CreatedAt, o.PaidAt, o.DeliveryTime())
		}
	}
	for _, q := range quotes {
		if activityvisibility.Visible(q.ID, q.TestMode) && (q.Status == "delivered" || q.Status == "fulfilled" || q.Status == "complete") {
			add(q.CreatedAt, q.PaidAt, q.DeliveryTime())
		}
	}
	out := map[string]interface{}{"delivered_count": count}
	if count > 0 {
		out["avg_delivery_seconds"] = total / float64(count)
	}
	return out
}
