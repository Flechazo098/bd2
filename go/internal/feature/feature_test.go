package feature

import (
	"errors"
	"testing"

	"bd2server/internal/wire"
)

func TestHandleAuditedEmptyResponses(t *testing.T) {
	if got := len(EmptyPacketCodes()); got != 32 {
		t.Fatalf("audited empty-response registry has %d paths, want 32", got)
	}
	for path, wantCode := range EmptyPacketCodes() {
		t.Run(path, func(t *testing.T) {
			gotCode, gotProto, ok, err := Handle(path, wire.AppendVarint(nil, 1, 42))
			if err != nil || !ok || gotCode != wantCode || len(gotProto) != 0 {
				t.Fatalf("Handle(%q): code=%d proto=%x ok=%t err=%v; want code=%d, empty, handled", path, gotCode, gotProto, ok, err, wantCode)
			}
		})
	}
}

func TestHandleRejectsUnknownAndInvalidRequests(t *testing.T) {
	if code, proto, ok, err := Handle("/not-a-real-endpoint", wire.AppendVarint(nil, 1, 1)); code != 0 || proto != nil || ok || err != nil {
		t.Fatalf("unknown route was not fail-closed: code=%d proto=%x ok=%t err=%v", code, proto, ok, err)
	}
	if _, _, ok, err := Handle("/CharAwakeInfo", wire.AppendVarint(nil, 1, 1)); ok || err != nil {
		t.Fatalf("stateful CharAwakeInfo must not be handled by feature defaults: ok=%v err=%v", ok, err)
	}
	for _, path := range []string{"/PresetInfo", "/DeckCostumeSettingInfo"} {
		if _, _, ok, err := Handle(path, wire.AppendVarint(nil, 1, 1)); ok || err != nil {
			t.Fatalf("stateful %s must not be handled by feature defaults: ok=%v err=%v", path, ok, err)
		}
	}
	for name, request := range map[string][]byte{
		"empty":         nil,
		"no-sequence":   wire.AppendVarint(nil, 2, 1),
		"zero-sequence": wire.AppendVarint(nil, 1, 0),
		"malformed":     {0x08, 0x80},
	} {
		t.Run(name, func(t *testing.T) {
			_, proto, ok, err := Handle("/QuestMaxClearInfo", request)
			if !ok || proto != nil || !errors.Is(err, ErrInvalidRequest) {
				t.Fatalf("invalid request: proto=%x ok=%t err=%v", proto, ok, err)
			}
		})
	}
}

func TestEmptyPacketCodesReturnsCopy(t *testing.T) {
	codes := EmptyPacketCodes()
	codes["/QuestMaxClearInfo"] = -1
	if code, _, ok, err := Handle("/QuestMaxClearInfo", wire.AppendVarint(nil, 1, 1)); err != nil || !ok || code != 137 {
		t.Fatalf("registry escaped its copy: code=%d ok=%t err=%v", code, ok, err)
	}
}

func TestStandaloneNativeDefaults(t *testing.T) {
	for path, want := range map[string]int{
		"/EventScheduleInfo": 163,
		"/EquipInfo":         34,
		"/TodayQuestInfo":    64,
		"/UpdateAgeGate":     0,
		"/ActiveMap":         0,
	} {
		code, proto, ok, err := Handle(path, wire.AppendVarint(nil, 1, 55))
		if err != nil || !ok || code != want || len(proto) != 0 {
			t.Fatalf("%s: code=%d proto=%x ok=%v err=%v", path, code, proto, ok, err)
		}
	}
}
