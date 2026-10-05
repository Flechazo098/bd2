package calendar

import (
	"bytes"
	"crypto/sha256"
	"encoding/binary"
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func TestInstalledBinaryFilesReencodeExactly(t *testing.T) {
	paths, e := filepath.Glob("../../../../schedules/*.bd2schedule")
	if e != nil || len(paths) != 6 {
		t.Fatalf("installed files=%d err=%v", len(paths), e)
	}
	for _, path := range paths {
		raw, e := os.ReadFile(path)
		if e != nil {
			t.Fatal(e)
		}
		m, e := UnmarshalBinary(raw)
		if e != nil {
			t.Fatal(e)
		}
		actual, e := MarshalBinary(m)
		if e != nil {
			t.Fatal(e)
		}
		if !bytes.Equal(raw, actual) {
			t.Fatalf("canonical bytes changed %s", path)
		}
	}
}
func TestBinaryRejectsInvalidPresence(t *testing.T) {
	raw, e := MarshalBinary(baseManifest())
	if e != nil {
		t.Fatal(e)
	}
	d := &decoder{data: raw[headerSize:]}
	d.str()
	d.str()
	d.str()
	d.count()
	d.count()
	d.count()
	raw[headerSize+d.pos] = 2
	sum := sha256.Sum256(raw[headerSize:])
	copy(raw[14:46], sum[:])
	if _, e = UnmarshalBinary(raw); e == nil {
		t.Fatal("invalid optional presence accepted despite valid checksum")
	}
}

func TestBinaryRoundTripAllRecords(t *testing.T) {
	m := baseManifest()
	m.Events = []Event{{UID: 5, Type: 11, ID: 7, Start: "2026-01-01T00:00:00Z", End: "2026-02-01T00:00:00Z"}}
	m.Gacha = []Gacha{{GroupID: 2, FreeCountBonus: true}}
	m.StepUps = []Gacha{{GroupID: 3}}
	m.Regular = &Regular{Contents: []Content{{ID: 4, Current: Season{ID: 8, Error: true}, Next: Season{ID: 9, Return: true}}}}
	m.MonsterHunt = &MonsterHunt{Seasons: []Hunt{{HuntID: 6, CostumeBanIDs: []uint64{5}, BurstBanIDs: []uint64{7}, IndependentFlag: true}}, History: []HuntHistory{{Season: 5, HuntID: 6, Hidden: true}}}
	m.CashProducts = []CashProduct{{GroupID: 1, ProductID: 2, EventIndex: 3}}
	m.EventHubs = []EventHub{{HubID: 4, Settings: []HubSetting{{Slot: 5, EventUIDs: []uint64{6, 7}}}}}
	m.MiniGameHubs = []MiniGameHub{{Slot: 8, EventUID: 5}}
	raw, e := MarshalBinary(m)
	if e != nil {
		t.Fatal(e)
	}
	actual, e := UnmarshalBinary(raw)
	if e != nil {
		t.Fatal(e)
	}
	if !reflect.DeepEqual(actual, m) {
		t.Fatalf("roundtrip changed records: %+v", actual)
	}
}
func TestBinaryRejectsCorruptionLimitsAndTrailing(t *testing.T) {
	raw, e := MarshalBinary(baseManifest())
	if e != nil {
		t.Fatal(e)
	}
	for n := 0; n < len(raw); n++ {
		if _, e = UnmarshalBinary(raw[:n]); e == nil {
			t.Fatalf("accepted truncation %d", n)
		}
	}
	for _, offset := range []int{0, 8, 10, 14, 46} {
		bad := append([]byte(nil), raw...)
		bad[offset] ^= 0xff
		if _, e = UnmarshalBinary(bad); e == nil {
			t.Fatalf("accepted corruption %d", offset)
		}
	}
	if _, e = UnmarshalBinary(append(raw, 0)); e == nil {
		t.Fatal("accepted trailing data")
	}
	m := baseManifest()
	m.Revision = string(make([]byte, maxString+1))
	if _, e = MarshalBinary(m); e == nil {
		t.Fatal("oversized string accepted")
	}
	m = baseManifest()
	m.Events = make([]Event, maxRows+1)
	if _, e = MarshalBinary(m); e == nil {
		t.Fatal("oversized count accepted")
	}
	bad := append([]byte(nil), raw...)
	binary.LittleEndian.PutUint32(bad[46:50], ^uint32(0))
	sum := sha256.Sum256(bad[46:])
	copy(bad[14:46], sum[:])
	if _, e = UnmarshalBinary(bad); e == nil {
		t.Fatal("overflow string length accepted")
	}
}
