package db

import (
	"encoding/hex"
	"encoding/json"
	"strconv"
	"strings"

	"github.com/dgraph-io/badger/v4"
)

// PaymentInspection reads original JSON, including legacy fields not represented
// by today's structs. It is diagnostic only: candidates never establish payment
// or overwrite a receipt. Only the authenticated operator detail exposes it.
type PaymentInspection struct {
	Records     []string          `json:"records"`
	Identifiers map[string]string `json:"identifiers"`
}

func collectPaymentIdentifiers(value any, path string, out map[string]string) {
	switch node := value.(type) {
	case map[string]any:
		for key, child := range node {
			name := strings.ToLower(strings.ReplaceAll(key, "_", ""))
			text, ok := child.(string)
			raw, hashError := hex.DecodeString(strings.TrimPrefix(strings.ToLower(text), "0x"))
			isHash := hashError == nil && len(raw) == 32 && !strings.Contains(name, "password") && (strings.HasSuffix(name, "hash") || name == "tx" || name == "txid" || name == "transactionid" || name == "fundingtx" || name == "settlementtx")
			isSwap := strings.HasSuffix(name, "swapid") || (name == "id" && strings.HasSuffix(strings.ToLower(path), ".swap"))
			if ok && text != "" && len(text) <= 256 && (isHash || isSwap) {
				out[path+"."+key] = text
			}
			collectPaymentIdentifiers(child, path+"."+key, out)
		}
	case []any:
		for index, child := range node {
			collectPaymentIdentifiers(child, path+"["+strconv.Itoa(index)+"]", out)
		}
	}
}

func (s *Store) InspectQuotePayment(id string) (PaymentInspection, error) {
	out := PaymentInspection{Records: []string{}, Identifiers: map[string]string{}}
	err := s.View(func(tx *badger.Txn) error {
		var quote map[string]any
		if err := getJSON(tx, quoteKey(id), &quote); err != nil {
			return err
		}
		out.Records = append(out.Records, "quote:"+id)
		collectPaymentIdentifiers(quote, "quote", out.Identifiers)
		supplier, _ := quote["supplier_order_id"].(string)
		options := badger.DefaultIteratorOptions
		options.Prefix = []byte("o:")
		iterator := tx.NewIterator(options)
		defer iterator.Close()
		for iterator.Rewind(); iterator.Valid(); iterator.Next() {
			var order map[string]any
			if err := iterator.Item().Value(func(raw []byte) error { return json.Unmarshal(raw, &order) }); err != nil {
				return err
			}
			if order["id"] != id && !(supplier != "" && order["supplier_order_id"] == supplier) && order["quote_id"] != id {
				continue
			}
			out.Records = append(out.Records, string(iterator.Item().Key()))
			collectPaymentIdentifiers(order, string(iterator.Item().Key()), out.Identifiers)
		}
		return nil
	})
	return out, err
}
