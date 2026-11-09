package db

// Stake groups are wallet-authenticated sharing groups, NOT merged shop
// accounts. User IDs, orders and payout addresses always remain independent.
// Each mutation atomically updates the group, all member user views and the
// boost spend budget. A signature must be verified before AddStakeAddress.
import (
	"errors"
	"fmt"
	"math"
	"sort"
	"time"

	"github.com/dgraph-io/badger/v4"
	"github.com/google/uuid"
	"nimiqshop/internal/nimiq"
	"nimiqshop/internal/stakeledger"
)

const stakeGroupMigrationKey = "meta:stake-groups:v1"

type stakeGroup struct {
	ID      string             `json:"id"`
	Members []StakeAddressLink `json:"members"`
}

func stakeGroupIndexKey(address string) []byte { return []byte("ix:sg:" + canonicalStakeAddr(address)) }
func stakeGroupKey(id string) []byte           { return []byte("sg:" + id) }
func stakeGroupSpendKey(id string) []byte      { return []byte("esg:" + id) }
func standaloneSpendStateKey(address string) []byte {
	return []byte("esc:" + canonicalStakeAddr(address))
}

// Missing index denotes a standalone wallet. A dangling index fails closed.
func stakeGroupInTx(tx *badger.Txn, address string) (stakeGroup, error) {
	id, err := getString(tx, stakeGroupIndexKey(address))
	if errors.Is(err, ErrNotFound) {
		return stakeGroup{Members: []StakeAddressLink{{Address: nimiq.NormalizeAddress(address)}}}, nil
	}
	if err != nil {
		return stakeGroup{}, err
	}
	var group stakeGroup
	if err = getJSON(tx, stakeGroupKey(id), &group); err != nil {
		return stakeGroup{}, err
	}
	if group.ID != id || len(group.Members) < 2 {
		return stakeGroup{}, errors.New("invalid stake group")
	}
	found := false
	for _, member := range group.Members {
		if canonicalStakeAddr(member.Address) == canonicalStakeAddr(address) {
			found = true
			break
		}
	}
	if !found {
		return stakeGroup{}, errors.New("stake group index does not match members")
	}
	return group, nil
}

func findOrCreateGroupUserInTx(tx *badger.Txn, address string, now time.Time) (User, error) {
	var u User
	id, err := getString(tx, userAddrIndexKey(address))
	if err == nil {
		err = getJSON(tx, userKey(id), &u)
		return u, err
	}
	if !errors.Is(err, ErrNotFound) {
		return u, err
	}
	u = User{ID: uuid.NewString(), NimiqAddress: nimiq.NormalizeAddress(address), CreatedAt: now.UTC()}
	raw, err := marshal(u)
	if err != nil {
		return u, err
	}
	if err = tx.Set(userKey(u.ID), raw); err != nil {
		return u, err
	}
	if err = tx.Set(userAddrIndexKey(address), []byte(u.ID)); err != nil {
		return u, err
	}
	return u, nil
}

func addCents(a, b int64) int64 {
	if a < 0 {
		a = 0
	}
	if b < 0 {
		b = 0
	}
	if a > math.MaxInt64-b {
		return math.MaxInt64
	}
	return a + b
}

// Install a set of members and its already-merged budget. Standalone users
// retain their budget too, so leaving/rejoining cannot refill today's limit.
func installStakeGroupInTx(tx *badger.Txn, members []StakeAddressLink, spend stakeSpendState, now time.Time) error {
	if len(members) == 0 {
		return nil
	}
	id := ""
	if len(members) > 1 {
		id = uuid.NewString()
	}
	group := stakeGroup{ID: id, Members: members}
	if id != "" {
		raw, err := marshal(group)
		if err != nil {
			return err
		}
		if err = tx.Set(stakeGroupKey(id), raw); err != nil {
			return err
		}
		raw, err = marshal(spend)
		if err != nil {
			return err
		}
		if err = tx.Set(stakeGroupSpendKey(id), raw); err != nil {
			return err
		}
	}
	for _, member := range members {
		u, err := findOrCreateGroupUserInTx(tx, member.Address, now)
		if err != nil {
			return err
		}
		u.StakeAddresses = nil
		for _, other := range members {
			if canonicalStakeAddr(other.Address) != canonicalStakeAddr(member.Address) {
				u.StakeAddresses = append(u.StakeAddresses, other)
			}
		}
		raw, err := marshal(u)
		if err != nil {
			return err
		}
		if err = tx.Set(userKey(u.ID), raw); err != nil {
			return err
		}
		if id == "" {
			if err = tx.Delete(stakeGroupIndexKey(member.Address)); err != nil {
				return err
			}
			raw, err = marshal(spend)
			if err != nil {
				return err
			}
			if err = tx.Set(standaloneSpendStateKey(member.Address), raw); err != nil {
				return err
			}
		} else if err = tx.Set(stakeGroupIndexKey(member.Address), []byte(id)); err != nil {
			return err
		}
	}
	return nil
}

func removeStakeGroupInTx(tx *badger.Txn, group stakeGroup) error {
	if group.ID == "" {
		return nil
	}
	if err := tx.Delete(stakeGroupKey(group.ID)); err != nil {
		return err
	}
	return tx.Delete(stakeGroupSpendKey(group.ID))
}

// Groups are indivisible when joining: cap is applied to the union, and
// previously consumed budgets are summed, never reset. Each old group counts
// exactly once even if several of its members appear in the input.
func mergeStakeGroupsInTx(tx *badger.Txn, addresses []string, at map[string]time.Time, now time.Time, enforceLimit bool) error {
	groups := make(map[string]stakeGroup)
	members := make(map[string]StakeAddressLink)
	for _, address := range addresses {
		group, err := stakeGroupInTx(tx, address)
		if err != nil {
			return err
		}
		key := group.ID
		if key == "" {
			key = "single:" + canonicalStakeAddr(address)
		}
		groups[key] = group
		for _, member := range group.Members {
			canon := canonicalStakeAddr(member.Address)
			if canon == "" {
				return errors.New("empty group member")
			}
			if date := at[canon]; member.LinkedAt.IsZero() && !date.IsZero() {
				member.LinkedAt = date
			}
			if member.LinkedAt.IsZero() {
				member.LinkedAt = now.UTC()
			}
			if old, ok := members[canon]; !ok || member.LinkedAt.Before(old.LinkedAt) {
				members[canon] = member
			}
		}
	}
	if enforceLimit && len(members) > MaxLinkedStakeAddresses+1 {
		return ErrTooManyStakeAddresses
	}
	merged := stakeSpendState{Components: map[string]stakeledger.SharedSpend{}}
	for _, group := range groups {
		ss, err := stakeSpendStateInTx(tx, group.Members[0].Address, now)
		if err != nil {
			return err
		}
		for key, component := range ss.Components {
			prev := merged.Components[key]
			prev.Rollover(now)
			if component.HDay > prev.HDay {
				prev.HDay = component.HDay
			}
			if component.HMonth > prev.HMonth {
				prev.HMonth = component.HMonth
			}
			merged.Components[key] = prev
		}
	}
	ordered := make([]StakeAddressLink, 0, len(members))
	for _, m := range members {
		ordered = append(ordered, m)
	}
	sort.Slice(ordered, func(i, j int) bool {
		if !ordered[i].LinkedAt.Equal(ordered[j].LinkedAt) {
			return ordered[i].LinkedAt.Before(ordered[j].LinkedAt)
		}
		return canonicalStakeAddr(ordered[i].Address) < canonicalStakeAddr(ordered[j].Address)
	})
	for _, group := range groups {
		if err := removeStakeGroupInTx(tx, group); err != nil {
			return err
		}
	}
	return installStakeGroupInTx(tx, ordered, merged, now)
}

// migrateStakeGroups converts the legacy directed links into connected
// components, one atomic component at a time. It runs before serving traffic;
// if interrupted, completed components remain intact and the scan is safe to
// repeat. Existing oversize components are retained, but cannot expand.
func (s *Store) migrateStakeGroups() error {
	type edge struct {
		a, b string
		at   time.Time
	}
	var edges []edge
	err := s.View(func(tx *badger.Txn) error {
		if _, err := getString(tx, []byte(stakeGroupMigrationKey)); err == nil {
			return nil
		} else if !errors.Is(err, ErrNotFound) {
			return err
		}
		return scanJSONPrefix(tx, []byte(prefixUser), func(item *badger.Item) error {
			var u User
			if err := item.Value(func(raw []byte) error { return unmarshal(raw, &u) }); err != nil {
				return err
			}
			if nimiq.ValidateAddress(u.NimiqAddress) != nil {
				return nil
			}
			for _, l := range u.LinkedStakeWallets() {
				if nimiq.ValidateAddress(l.Address) == nil {
					edges = append(edges, edge{u.NimiqAddress, l.Address, l.LinkedAt})
				}
			}
			return nil
		})
	})
	if err != nil {
		return err
	}
	parent := map[string]string{}
	var root func(string) string
	root = func(x string) string {
		if parent[x] == "" {
			parent[x] = x
		}
		if parent[x] != x {
			parent[x] = root(parent[x])
		}
		return parent[x]
	}
	for _, e := range edges {
		a, b := canonicalStakeAddr(e.a), canonicalStakeAddr(e.b)
		ra, rb := root(a), root(b)
		if ra != rb {
			parent[rb] = ra
		}
	}
	components := map[string][]string{}
	dates := map[string]time.Time{}
	for _, e := range edges {
		for _, a := range []string{e.a, e.b} {
			c := canonicalStakeAddr(a)
			if !e.at.IsZero() && (dates[c].IsZero() || e.at.Before(dates[c])) {
				dates[c] = e.at
			}
		}
	}
	for a := range parent {
		r := root(a)
		components[r] = append(components[r], nimiq.NormalizeAddress(a))
	}
	now := time.Now().UTC()
	for _, addrs := range components {
		err = s.Update(func(tx *badger.Txn) error {
			if len(addrs) < 2 {
				return nil
			}
			// A prior interrupted attempt may have already committed this component.
			first, err := stakeGroupInTx(tx, addrs[0])
			if err != nil {
				return err
			}
			if first.ID != "" && len(first.Members) == len(addrs) {
				complete := true
				for _, a := range addrs {
					g, e := stakeGroupInTx(tx, a)
					if e != nil {
						return e
					}
					if g.ID != first.ID {
						complete = false
						break
					}
				}
				if complete {
					return nil
				}
			}
			return mergeStakeGroupsInTx(tx, addrs, dates, now, false)
		})
		if err != nil {
			return fmt.Errorf("migrate linked stake wallets: %w", err)
		}
	}
	return s.Update(func(tx *badger.Txn) error { return tx.Set([]byte(stakeGroupMigrationKey), []byte("1")) })
}
