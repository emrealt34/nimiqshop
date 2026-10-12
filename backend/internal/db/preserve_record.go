package db

import (
	"encoding/json"
	"errors"
	"reflect"
	"strings"

	"github.com/dgraph-io/badger/v4"
)

// marshalPreservingUnknown retains legacy extension fields when a typed order
// is updated. Known omitted fields are removed deliberately: preserving them
// would resurrect cleared addresses, comments or payment flags.
func marshalPreservingUnknown(tx *badger.Txn, key []byte, value any) ([]byte, error) {
	current, err := marshal(value)
	if err != nil {
		return nil, err
	}
	var previous map[string]json.RawMessage
	if err := getJSON(tx, key, &previous); errors.Is(err, ErrNotFound) {
		return current, nil
	} else if err != nil {
		return nil, err
	}
	typeOf := reflect.TypeOf(value)
	if typeOf.Kind() == reflect.Pointer {
		typeOf = typeOf.Elem()
	}
	for index := 0; index < typeOf.NumField(); index++ {
		field := typeOf.Field(index)
		name := strings.Split(field.Tag.Get("json"), ",")[0]
		if name == "" {
			name = field.Name
		}
		delete(previous, name)
	}
	var updated map[string]json.RawMessage
	if err := json.Unmarshal(current, &updated); err != nil {
		return nil, err
	}
	for name, raw := range updated {
		previous[name] = raw
	}
	return json.Marshal(previous)
}
