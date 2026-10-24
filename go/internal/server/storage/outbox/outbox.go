package outbox

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"

	"bd2server/internal/server/domain/command"
	"bd2server/internal/server/storage/stateio"
)

const (
	Domain        = "outbox"
	pendingBucket = "pending"
	receiptBucket = "published"
	MaxPending    = 256
	MaxPayload    = 64 << 10
)

var ErrFull = errors.New("outbox pending capacity exhausted")
var ErrConflict = errors.New("outbox event identity has conflicting content")

type Envelope struct {
	Version int           `json:"version"`
	Source  string        `json:"source"`
	Event   command.Event `json:"event"`
}

type PageStore interface {
	stateio.AtomicEntryStore
	EntryPage(domain, bucket, after string, limit int) ([]stateio.Entry, error)
	EntryCount(domain, bucket string) (int64, error)
}

type Pending struct {
	Key      string   `json:"key"`
	Digest   string   `json:"digest"`
	Envelope Envelope `json:"envelope"`
}

func Publish(tx PageStore, source string, event command.Event) error {
	if tx == nil || !validEnvelope(Envelope{Version: 1, Source: source, Event: event}) {
		return errors.New("outbox requires a source, event identity, topic and bounded JSON payload")
	}
	envelope := Envelope{Version: 1, Source: source, Event: event}
	raw, err := json.Marshal(envelope)
	if err != nil {
		return err
	}
	key := identity(source, event.Topic, event.ID)
	digest := sha256.Sum256(raw)
	previous, found, err := tx.LoadEntry(Domain, receiptBucket, key)
	if err != nil {
		return err
	}
	if found {
		if !bytes.Equal(previous, digest[:]) {
			return fmt.Errorf("%w: %s event %q from %s was published with a different body", ErrConflict, event.Topic, event.ID, source)
		}
		return nil
	}
	count, err := tx.EntryCount(Domain, pendingBucket)
	if err != nil {
		return err
	}
	if count >= MaxPending {
		return fmt.Errorf("%w: %d events are pending", ErrFull, count)
	}
	sequenceRaw, found, err := tx.LoadEntry(Domain, "sequence", "last")
	if err != nil {
		return err
	}
	var sequence uint64
	if found {
		sequence, err = strconv.ParseUint(string(sequenceRaw), 10, 64)
		if err != nil {
			return errors.New("outbox sequence is corrupt")
		}
	}
	if sequence == ^uint64(0) {
		return errors.New("outbox sequence exhausted")
	}
	sequence++
	pendingKey := fmt.Sprintf("%020d:%s", sequence, key)
	if err := tx.PutEntry(Domain, pendingBucket, pendingKey, raw); err != nil {
		return err
	}
	if err := tx.PutEntry(Domain, "sequence", "last", []byte(strconv.FormatUint(sequence, 10))); err != nil {
		return err
	}
	return tx.PutEntry(Domain, receiptBucket, key, digest[:])
}

func validEnvelope(e Envelope) bool {
	return e.Version == 1 && e.Source != "" && len(e.Source) <= 256 && !strings.ContainsRune(e.Source, '\x00') &&
		e.Event.ID != "" && len(e.Event.ID) <= 512 && e.Event.Topic != "" && len(e.Event.Topic) <= 128 &&
		len(e.Event.Payload) <= MaxPayload && json.Valid(e.Event.Payload)
}

// Validate binds the delivery cursor and digest to the immutable event identity.
func Validate(p Pending) (string, []byte, error) {
	key, ok := pendingIdentity(p.Key)
	if !ok || !validEnvelope(p.Envelope) || key != identity(p.Envelope.Source, p.Envelope.Event.Topic, p.Envelope.Event.ID) {
		return "", nil, fmt.Errorf("%w: pending event %q does not match its envelope", ErrConflict, p.Key)
	}
	raw, err := json.Marshal(p.Envelope)
	if err != nil {
		return "", nil, err
	}
	digest := sha256.Sum256(raw)
	if p.Digest != hex.EncodeToString(digest[:]) {
		return "", nil, fmt.Errorf("%w: pending event %q digest does not match its envelope", ErrConflict, p.Key)
	}
	return key, digest[:], nil
}

func Read(tx PageStore, limit int) ([]Pending, error) {
	entries, err := tx.EntryPage(Domain, pendingBucket, "", limit)
	if err != nil {
		return nil, err
	}
	out := make([]Pending, 0, len(entries))
	for _, entry := range entries {
		var envelope Envelope
		identityKey, valid := pendingIdentity(entry.Key)
		if err := json.Unmarshal(entry.Payload, &envelope); err != nil || !valid || !validEnvelope(envelope) ||
			identityKey != identity(envelope.Source, envelope.Event.Topic, envelope.Event.ID) {
			return nil, fmt.Errorf("outbox contains an invalid event %s", entry.Key)
		}
		digest := sha256.Sum256(entry.Payload)
		out = append(out, Pending{Key: entry.Key, Digest: hex.EncodeToString(digest[:]), Envelope: envelope})
	}
	return out, nil
}

func Acknowledge(tx stateio.AtomicEntryStore, key, digest string) error {
	identityKey, valid := pendingIdentity(key)
	expected, err := hex.DecodeString(digest)
	if err != nil || len(expected) != sha256.Size || !valid {
		return errors.New("invalid outbox acknowledgement")
	}
	published, found, err := tx.LoadEntry(Domain, receiptBucket, identityKey)
	if err != nil {
		return err
	}
	if !found || !bytes.Equal(published, expected) {
		return fmt.Errorf("%w: acknowledgement for %q does not match the published event (present=%t)", ErrConflict, key, found)
	}
	_, err = tx.DeleteEntry(Domain, pendingBucket, key)
	return err
}

func pendingIdentity(key string) (string, bool) {
	if len(key) != 20+1+sha256.Size*2 || key[20] != ':' {
		return "", false
	}
	sequence, err := strconv.ParseUint(key[:20], 10, 64)
	if err != nil || sequence == 0 {
		return "", false
	}
	id := key[21:]
	decoded, err := hex.DecodeString(id)
	return id, err == nil && len(decoded) == sha256.Size
}

func identity(source, topic, id string) string {
	raw, _ := json.Marshal([3]string{source, topic, id})
	digest := sha256.Sum256(raw)
	return hex.EncodeToString(digest[:])
}
