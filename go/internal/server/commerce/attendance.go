package commerce

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"

	"bd2server/internal/server/stateio"
	"bd2server/internal/server/wire"
)

type attendanceHandler interface {
	Handle(string, []byte) (int, []byte, bool, error)
}

// AttendanceHandler preserves the original event progress response and adds
// subscription grants in extension fields understood by the commerce plugin.
// All operations execute inside the transport's account transaction.
type AttendanceHandler struct {
	Events      attendanceHandler
	Economy     *EntitlementEconomy
	LoginPasses *LoginPasses
	Store       stateio.Store
}

type attendanceReceipt struct {
	Digest string `json:"digest"`
	Bundle []byte `json:"bundle"`
}

func (h AttendanceHandler) Handle(path string, request []byte) (int, []byte, bool, error) {
	return h.HandleSession(path, request, "")
}

func (h AttendanceHandler) HandleSession(path string, request []byte, session string) (int, []byte, bool, error) {
	if path != "/Attendance" {
		return 0, nil, false, nil
	}
	if session == "" || h.Events == nil || h.Economy == nil || h.Store == nil {
		return 0, nil, true, fmt.Errorf("commerce: attendance dependencies/session unavailable")
	}
	seq, found, err := wire.Varint(request, 1)
	if err != nil || !found || seq == 0 {
		return 0, nil, true, fmt.Errorf("commerce: missing attendance sequence")
	}
	code, response, handled, err := h.Events.Handle(path, request)
	if err != nil || !handled {
		return code, response, handled, err
	}
	// The event handler may already have granted ordinary attendance rewards.
	// The client accepts exactly one reward envelope, so combine every grant in
	// execution order under this wrapper's replay identity.
	response, eventBundle, err := takeAttendanceRewardEnvelope(response)
	if err != nil {
		return code, nil, true, err
	}
	key := fmt.Sprintf("commerce_attendance:%x:%d", sha256.Sum256([]byte(session)), seq)
	digest := fmt.Sprintf("%x", sha256.Sum256(request))
	var receipt attendanceReceipt
	previous, err := h.Store.Load(key)
	if err != nil {
		return code, nil, true, err
	}
	var loginBundle []byte
	if h.LoginPasses != nil {
		var infos [][]byte
		loginBundle, infos, err = h.LoginPasses.ClaimAndInfo(key)
		if err != nil {
			return code, nil, true, err
		}
		for _, info := range infos {
			response = wire.AppendBytes(response, 6, info)
		}
	}
	if previous != nil {
		if err = json.Unmarshal(previous, &receipt); err != nil || receipt.Digest != digest {
			return code, nil, true, fmt.Errorf("commerce: conflicting attendance replay")
		}
	} else {
		receipt.Digest = digest
		subscriptionBundle, err := h.Economy.ClaimSubscriptions(key)
		if err != nil {
			return code, nil, true, err
		}
		// Preserve events -> login pass -> subscription execution order and all
		// repeated reward entries in the single client envelope.
		receipt.Bundle = append(receipt.Bundle, eventBundle...)
		receipt.Bundle = append(receipt.Bundle, loginBundle...)
		receipt.Bundle = append(receipt.Bundle, subscriptionBundle...)
		raw, err := json.Marshal(receipt)
		if err != nil {
			return code, nil, true, err
		}
		if err = h.Store.Save(key, raw); err != nil {
			return code, nil, true, err
		}
	}
	response, err = h.Economy.MergeAttendance(response)
	if err != nil {
		return code, nil, true, err
	}
	if len(receipt.Bundle) != 0 {
		response = wire.AppendBytes(response, 1001, receipt.Bundle)
		response = wire.AppendString(response, 1002, key)
	}
	return code, response, true, nil
}

// Strip the child envelope before adding the combined one. Keep native and
// unrelated unknown fields byte-for-byte; reject ambiguous child envelopes
// rather than returning a response the client would silently ignore.
func takeAttendanceRewardEnvelope(response []byte) ([]byte, []byte, error) {
	var native, bundle []byte
	var receipt string
	var hasBundle, hasReceipt bool
	err := wire.Walk(response, func(f wire.Field) error {
		switch f.Number {
		case 1001:
			if f.Type != 2 || hasBundle {
				return fmt.Errorf("commerce: ambiguous attendance reward bundle")
			}
			hasBundle = true
			bundle = append([]byte(nil), f.Value...)
		case 1002:
			if f.Type != 2 || hasReceipt {
				return fmt.Errorf("commerce: ambiguous attendance reward receipt")
			}
			hasReceipt = true
			receipt = string(f.Value)
		default:
			native = append(native, response[f.Start:f.End]...)
		}
		return nil
	})
	if err != nil {
		return nil, nil, err
	}
	if hasBundle != hasReceipt || hasReceipt && (receipt == "" || len(receipt) > 1024) {
		return nil, nil, fmt.Errorf("commerce: incomplete attendance reward envelope")
	}
	return native, bundle, nil
}
