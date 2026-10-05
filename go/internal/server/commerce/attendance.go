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
		receipt.Bundle, err = h.Economy.ClaimSubscriptions(key)
		if err != nil {
			return code, nil, true, err
		}
		receipt.Bundle = append(receipt.Bundle, loginBundle...)
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
