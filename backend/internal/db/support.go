package db

import (
	"errors"
	"strings"
	"time"

	"github.com/dgraph-io/badger/v4"
	"github.com/google/uuid"
)

// CreateSupportTicket creates a new ticket and its first message atomically in BadgerDB.
func (s *Store) CreateSupportTicket(t SupportTicket, firstMessage string) (SupportTicket, SupportMessage, error) {
	if strings.TrimSpace(t.UserID) == "" || strings.TrimSpace(t.OrderID) == "" || strings.TrimSpace(t.Subject) == "" || strings.TrimSpace(firstMessage) == "" {
		return SupportTicket{}, SupportMessage{}, errors.New("missing required ticket fields")
	}

	now := time.Now().UTC()
	if t.ID == "" {
		t.ID = uuid.NewString()
	}
	t.CreatedAt = now
	t.UpdatedAt = now
	t.Status = "open"
	t.LastMessageBy = "user"
	t.MessageCount = 1

	// Truncate snippet if too long
	snippet := strings.TrimSpace(firstMessage)
	if len(snippet) > 120 {
		snippet = snippet[:117] + "..."
	}
	t.LastMessageSnippet = snippet

	msg := SupportMessage{
		ID:        uuid.NewString(),
		TicketID:  t.ID,
		OrderID:   t.OrderID,
		Sender:    "user",
		SenderID:  t.UserID,
		Message:   strings.TrimSpace(firstMessage),
		CreatedAt: now,
	}

	err := s.Update(func(txn *badger.Txn) error {
		// Ticket record
		rawTicket, err := marshal(t)
		if err != nil {
			return err
		}
		if err := txn.Set(supportTicketKey(t.ID), rawTicket); err != nil {
			return err
		}

		// Secondary indexes
		if err := txn.Set(supportTicketUserIndexKey(t.UserID, t.CreatedAt.UnixNano(), t.ID), []byte(t.ID)); err != nil {
			return err
		}
		if err := txn.Set(supportTicketOrderIndexKey(t.OrderID, t.ID), []byte(t.ID)); err != nil {
			return err
		}
		if err := txn.Set(supportTicketStatusIndexKey(t.Status, t.CreatedAt.UnixNano(), t.ID), []byte(t.ID)); err != nil {
			return err
		}
		if err := txn.Set(supportTicketAllIndexKey(t.CreatedAt.UnixNano(), t.ID), []byte(t.ID)); err != nil {
			return err
		}

		// First message
		rawMsg, err := marshal(msg)
		if err != nil {
			return err
		}
		if err := txn.Set(supportMessageKey(msg.ID), rawMsg); err != nil {
			return err
		}
		return txn.Set(supportMessageTicketIndexKey(t.ID, msg.CreatedAt.UnixNano(), msg.ID), []byte(msg.ID))
	})

	return t, msg, err
}

// GetSupportTicket returns a ticket by its ID.
func (s *Store) GetSupportTicket(id string) (SupportTicket, error) {
	var t SupportTicket
	err := s.View(func(txn *badger.Txn) error {
		return getJSON(txn, supportTicketKey(id), &t)
	})
	return t, err
}

// GetSupportTicketForUser returns a ticket by ID only if owned by userID.
func (s *Store) GetSupportTicketForUser(id, userID string) (SupportTicket, error) {
	t, err := s.GetSupportTicket(id)
	if err != nil {
		return t, err
	}
	if t.UserID != userID {
		return SupportTicket{}, ErrNotFound
	}
	return t, nil
}

// GetSupportTicketForOrder returns the most recent ticket opened for a specific order.
func (s *Store) GetSupportTicketForOrder(orderID string) (SupportTicket, error) {
	var ticketID string
	err := s.View(func(txn *badger.Txn) error {
		return scanIndex(txn, supportTicketOrderIndexPrefix(orderID), 1, func(id string) error {
			ticketID = id
			return nil
		})
	})
	if err != nil {
		return SupportTicket{}, err
	}
	if ticketID == "" {
		return SupportTicket{}, ErrNotFound
	}
	return s.GetSupportTicket(ticketID)
}

// ListSupportTicketsForUser lists all tickets for a specific user, newest-first.
func (s *Store) ListSupportTicketsForUser(userID string, limit int) ([]SupportTicket, error) {
	var out []SupportTicket
	err := s.View(func(txn *badger.Txn) error {
		return scanIndex(txn, supportTicketUserIndexPrefix(userID), limit, func(id string) error {
			var t SupportTicket
			if err := getJSON(txn, supportTicketKey(id), &t); err != nil {
				if errors.Is(err, ErrNotFound) {
					return nil
				}
				return err
			}
			out = append(out, t)
			return nil
		})
	})
	return out, err
}

// ListAllSupportTickets lists tickets with optional status filter, newest-first.
func (s *Store) ListAllSupportTickets(statusFilter string, limit int) ([]SupportTicket, error) {
	var out []SupportTicket
	prefix := supportTicketAllIndexPrefix()
	if statusFilter != "" && statusFilter != "all" {
		prefix = supportTicketStatusIndexPrefix(statusFilter)
	}

	err := s.View(func(txn *badger.Txn) error {
		return scanIndex(txn, prefix, limit, func(id string) error {
			var t SupportTicket
			if err := getJSON(txn, supportTicketKey(id), &t); err != nil {
				if errors.Is(err, ErrNotFound) {
					return nil
				}
				return err
			}
			out = append(out, t)
			return nil
		})
	})
	return out, err
}

// AddSupportMessage adds a message to an existing ticket and updates ticket metadata.
func (s *Store) AddSupportMessage(ticketID, sender, senderID, messageText, newStatus string) (SupportMessage, error) {
	if strings.TrimSpace(messageText) == "" {
		return SupportMessage{}, errors.New("message cannot be empty")
	}

	now := time.Now().UTC()
	msg := SupportMessage{
		ID:        uuid.NewString(),
		TicketID:  ticketID,
		Sender:    sender,
		SenderID:  senderID,
		Message:   strings.TrimSpace(messageText),
		CreatedAt: now,
	}

	err := s.Update(func(txn *badger.Txn) error {
		var t SupportTicket
		if err := getJSON(txn, supportTicketKey(ticketID), &t); err != nil {
			return err
		}

		oldStatus := t.Status
		if newStatus != "" {
			t.Status = newStatus
		} else if sender == "admin" {
			t.Status = "waiting_user"
		} else {
			t.Status = "waiting_admin"
		}

		t.UpdatedAt = now
		t.LastMessageBy = sender
		t.MessageCount++

		snippet := strings.TrimSpace(messageText)
		if len(snippet) > 120 {
			snippet = snippet[:117] + "..."
		}
		t.LastMessageSnippet = snippet

		msg.OrderID = t.OrderID

		// Update ticket
		rawTicket, err := marshal(t)
		if err != nil {
			return err
		}
		if err := txn.Set(supportTicketKey(t.ID), rawTicket); err != nil {
			return err
		}

		// Update status index if changed
		if oldStatus != t.Status {
			_ = txn.Delete(supportTicketStatusIndexKey(oldStatus, t.CreatedAt.UnixNano(), t.ID))
			if err := txn.Set(supportTicketStatusIndexKey(t.Status, t.CreatedAt.UnixNano(), t.ID), []byte(t.ID)); err != nil {
				return err
			}
		}

		// Save message
		rawMsg, err := marshal(msg)
		if err != nil {
			return err
		}
		if err := txn.Set(supportMessageKey(msg.ID), rawMsg); err != nil {
			return err
		}
		return txn.Set(supportMessageTicketIndexKey(ticketID, msg.CreatedAt.UnixNano(), msg.ID), []byte(msg.ID))
	})

	return msg, err
}

// GetTicketMessages returns all messages for a ticket in ascending chronological order.
func (s *Store) GetTicketMessages(ticketID string) ([]SupportMessage, error) {
	var out []SupportMessage
	prefix := supportMessageTicketIndexPrefix(ticketID)

	err := s.View(func(txn *badger.Txn) error {
		opts := badger.DefaultIteratorOptions
		opts.Prefix = prefix
		opts.PrefetchValues = true
		it := txn.NewIterator(opts)
		defer it.Close()

		for it.Rewind(); it.ValidForPrefix(prefix); it.Next() {
			var msgID string
			if err := it.Item().Value(func(v []byte) error {
				msgID = string(v)
				return nil
			}); err != nil {
				return err
			}

			var msg SupportMessage
			if err := getJSON(txn, supportMessageKey(msgID), &msg); err != nil {
				if errors.Is(err, ErrNotFound) {
					continue
				}
				return err
			}
			out = append(out, msg)
		}
		return nil
	})

	return out, err
}

// UpdateSupportTicketStatus updates status of a ticket.
func (s *Store) UpdateSupportTicketStatus(ticketID, newStatus string) error {
	now := time.Now().UTC()
	return s.Update(func(txn *badger.Txn) error {
		var t SupportTicket
		if err := getJSON(txn, supportTicketKey(ticketID), &t); err != nil {
			return err
		}
		if t.Status == newStatus {
			return nil
		}

		oldStatus := t.Status
		t.Status = newStatus
		t.UpdatedAt = now

		rawTicket, err := marshal(t)
		if err != nil {
			return err
		}
		if err := txn.Set(supportTicketKey(t.ID), rawTicket); err != nil {
			return err
		}

		_ = txn.Delete(supportTicketStatusIndexKey(oldStatus, t.CreatedAt.UnixNano(), t.ID))
		return txn.Set(supportTicketStatusIndexKey(newStatus, t.CreatedAt.UnixNano(), t.ID), []byte(t.ID))
	})
}

// CountOpenSupportTickets counts tickets that are not resolved or closed.
func (s *Store) CountOpenSupportTickets() (int, error) {
	openCount := 0
	err := s.View(func(txn *badger.Txn) error {
		return scanIndex(txn, supportTicketAllIndexPrefix(), 500, func(id string) error {
			var t SupportTicket
			if err := getJSON(txn, supportTicketKey(id), &t); err == nil {
				if t.Status != "resolved" && t.Status != "closed" {
					openCount++
				}
			}
			return nil
		})
	})
	return openCount, err
}
