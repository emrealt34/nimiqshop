package db

import (
	"errors"
	"fmt"

	"nimiqshop/internal/catalog"

	"github.com/dgraph-io/badger/v4"
)

// catalogRulesKey stores the operator catalog policy as one JSON blob.
// Badger writes are atomic single-key commits, so a crash mid-save either
// keeps the previous rules or the new ones — never a torn mixture.
const catalogRulesKey = "meta:admin:catalog-rules"

// GetCatalogRules returns the persisted rules; a missing record yields the
// normalized zero rules (open catalog), never an error.
func (s *Store) GetCatalogRules() (catalog.Rules, error) {
	rules := catalog.Rules{}
	err := s.View(func(txn *badger.Txn) error {
		err := getJSON(txn, []byte(catalogRulesKey), &rules)
		if errors.Is(err, ErrNotFound) {
			return nil
		}
		return err
	})
	if err != nil {
		return catalog.Rules{}, err
	}
	rules.Normalize()
	return rules, nil
}

// SetCatalogRules validates + persists the rules atomically.
func (s *Store) SetCatalogRules(rules catalog.Rules) (catalog.Rules, error) {
	if rules.MaxFaceValueUSD < 0 || rules.MaxFaceValueUSD > 1_000_000 {
		return catalog.Rules{}, fmt.Errorf("max face value must be between 0 (off) and 1,000,000")
	}
	if len(rules.HiddenFamilies) > 5000 || len(rules.BannedCategories) > 500 || len(rules.BannedKinds) > 50 || len(rules.HiddenCountries) > 300 || len(rules.VisibleCountries) > 300 {
		return catalog.Rules{}, fmt.Errorf("rule lists are too large")
	}
	rules.Normalize()
	err := s.Update(func(txn *badger.Txn) error {
		blob, err := marshal(rules)
		if err != nil {
			return err
		}
		return txn.Set([]byte(catalogRulesKey), blob)
	})
	if err != nil {
		return catalog.Rules{}, err
	}
	return rules, nil
}
