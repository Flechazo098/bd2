package roster

import (
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"hash/crc32"
	"io"
	"math"
	"slices"
	"strconv"
	"strings"
	"unicode/utf8"

	"bd2server/internal/common/util"
	"bd2server/internal/server/design/gamedata"
	"bd2server/internal/server/domain/command"
	"bd2server/internal/server/protocol/wire"
	"bd2server/internal/server/storage/stateio"
)

var ErrRecordNotFound = errors.New("interaction recording not found")

const recordDomain = "collection"
const recordMetadataBucket = "spine_record_metadata"
const recordDataBucket = "spine_record_data"

type InteractionRecordService struct {
	Store         stateio.ScopedEntryStore
	Design        *gamedata.SpineInteractionDesign
	Authorize     func(command.Context, uint64) (bool, error)
	RecordDataURL func(command.Context, uint64, string) (string, error)
	CheckText     func(command.Context, string, string) error
}

type interactionRecording struct {
	Index      uint64 `json:"index"`
	Group      uint64 `json:"group"`
	Name       string `json:"name"`
	Capability string `json:"capability"`
	Digest     string `json:"digest"`
}

func (s InteractionRecordService) authorize(ctx command.Context, group uint64) error {
	if s.Design == nil || s.Authorize == nil || !s.Design.RecordGroups[group] {
		return ErrRecordNotFound
	}
	allowed, err := s.Authorize(ctx, group)
	if err != nil {
		return err
	}
	if !allowed {
		return ErrRecordNotFound
	}
	return nil
}

func (s InteractionRecordService) metadata(ctx command.Context, index uint64) (interactionRecording, error) {
	var row interactionRecording
	if s.Store == nil || ctx.State == nil || index == 0 || index > math.MaxInt32 {
		return row, ErrRecordNotFound
	}
	raw, found, err := s.Store.LoadEntry(ctx.State, recordDomain, recordMetadataBucket, strconv.FormatUint(index, 10))
	if err != nil {
		return row, err
	}
	if !found {
		return row, ErrRecordNotFound
	}
	return decodeInteractionRecording(raw, index)
}

func decodeInteractionRecording(raw []byte, index uint64) (interactionRecording, error) {
	var row interactionRecording
	if err := stateio.RequireExactJSONObject(raw, "index", "group", "name", "capability", "digest"); err != nil {
		return row, err
	}
	if err := json.Unmarshal(raw, &row); err != nil {
		return row, err
	}
	capability, capErr := base64.RawURLEncoding.DecodeString(row.Capability)
	digest, digestErr := hex.DecodeString(row.Digest)
	if row.Index != index || row.Group == 0 || row.Name == "" || capErr != nil || len(capability) != 32 || digestErr != nil || len(digest) != sha256.Size {
		return row, fmt.Errorf("interaction recording: corrupt metadata")
	}
	return row, nil
}

// LoadRecordData authenticates a download capability inside the account's command scope.
func (s InteractionRecordService) LoadRecordData(ctx command.Context, index uint64, capability string) ([]byte, error) {
	row, err := s.metadata(ctx, index)
	if err != nil {
		return nil, err
	}
	if subtle.ConstantTimeCompare([]byte(row.Capability), []byte(capability)) != 1 {
		return nil, ErrRecordNotFound
	}
	if err = s.authorize(ctx, row.Group); err != nil {
		return nil, err
	}
	return s.recordData(ctx, row)
}

func (s InteractionRecordService) recordData(ctx command.Context, row interactionRecording) ([]byte, error) {
	raw, found, err := s.Store.LoadEntry(ctx.State, recordDomain, recordDataBucket, strconv.FormatUint(row.Index, 10))
	if err != nil {
		return nil, err
	}
	if !found {
		return nil, ErrRecordNotFound
	}
	digest := sha256.Sum256(raw)
	if hex.EncodeToString(digest[:]) != row.Digest {
		return nil, fmt.Errorf("interaction recording: corrupt payload")
	}
	return raw, nil
}

func (s InteractionRecordService) url(ctx command.Context, row interactionRecording) (string, error) {
	if s.RecordDataURL == nil {
		return "", fmt.Errorf("interaction recording: missing download URL provider")
	}
	url, err := s.RecordDataURL(ctx, row.Index, row.Capability)
	if err == nil && url == "" {
		err = fmt.Errorf("interaction recording: empty download URL")
	}
	return url, err
}

func (s InteractionRecordService) Handle(ctx command.Context, path string, request []byte) (int, []byte, bool, error) {
	codes := map[string]int{"/SpineInteractionRecordInfo": 571, "/SpineInteractionRecordDetailInfo": 572, "/SpineInteractionRecordSave": 573, "/SpineInteractionRecordDelete": 574, "/SpineInteractionRecordDataUpdate": 575, "/SpineInteractionRecordNameUpdate": 576}
	code, found := codes[path]
	if !found {
		return 0, nil, false, nil
	}
	fail := func(err error) (int, []byte, bool, error) { return code, nil, true, err }
	if s.Store == nil || s.Design == nil || s.Authorize == nil || ctx.State == nil {
		return fail(fmt.Errorf("interaction recording: missing dependencies"))
	}
	seq, found, err := wire.Varint(request, 1)
	if err != nil || !found || seq == 0 || seq > math.MaxInt32 || len(request) > (4<<20)+1024 {
		return fail(fmt.Errorf("interaction recording: invalid request"))
	}
	if path == "/SpineInteractionRecordInfo" {
		entries, err := s.Store.ListEntries(ctx.State, recordDomain, recordMetadataBucket)
		if err != nil {
			return fail(err)
		}
		indices := make([]uint64, 0, len(entries))
		for key := range entries {
			n, err := strconv.ParseUint(key, 10, 31)
			if err != nil || n == 0 {
				return fail(fmt.Errorf("interaction recording: corrupt index"))
			}
			indices = append(indices, n)
		}
		slices.Sort(indices)
		var out []byte
		authorized := map[uint64]bool{}
		for _, index := range indices {
			row, err := decodeInteractionRecording(entries[strconv.FormatUint(index, 10)], index)
			if err != nil {
				return fail(err)
			}
			if allowed, checked := authorized[row.Group]; checked {
				if !allowed {
					continue
				}
			} else {
				err = s.authorize(ctx, row.Group)
				if errors.Is(err, ErrRecordNotFound) {
					authorized[row.Group] = false
					continue
				} else if err != nil {
					return fail(err)
				}
				authorized[row.Group] = true
			}
			url, err := s.url(ctx, row)
			if err != nil {
				return fail(err)
			}
			p := wire.AppendVarint(nil, 1, row.Index)
			p = wire.AppendVarint(p, 2, row.Group)
			p = wire.AppendBytes(p, 3, []byte(row.Name))
			p = wire.AppendBytes(p, 4, []byte(url))
			out = wire.AppendBytes(out, 1, p)
		}
		return code, out, true, nil
	}
	index, _, err := wire.Varint(request, 2)
	if err != nil || index == 0 || index > math.MaxInt32 {
		return fail(ErrRecordNotFound)
	}
	var row interactionRecording
	var counter []byte
	if path == "/SpineInteractionRecordSave" {
		if err = s.authorize(ctx, index); err != nil {
			return fail(err)
		}
		entries, err := s.Store.ListEntries(ctx.State, recordDomain, recordMetadataBucket)
		if err != nil {
			return fail(err)
		}
		count := uint64(0)
		for key, payload := range entries {
			n, err := strconv.ParseUint(key, 10, 31)
			if err != nil {
				return fail(err)
			}
			r, err := decodeInteractionRecording(payload, n)
			if err != nil {
				return fail(err)
			}
			if r.Group == index {
				count++
			}
		}
		if count >= s.Design.RecordSaveSlots {
			return fail(fmt.Errorf("interaction recording: save slots full"))
		}
		raw, exists, err := s.Store.LoadEntry(ctx.State, recordDomain, "spine_record_counter", "next")
		if err != nil {
			return fail(err)
		}
		next := uint64(1)
		if exists {
			next, err = strconv.ParseUint(string(raw), 10, 31)
			if err != nil || next == 0 {
				return fail(fmt.Errorf("interaction recording: corrupt counter"))
			}
		}
		if next >= math.MaxInt32 {
			return fail(fmt.Errorf("interaction recording: index exhausted"))
		}
		if _, exists := entries[strconv.FormatUint(next, 10)]; exists {
			return fail(fmt.Errorf("interaction recording: counter collision"))
		}
		row = interactionRecording{Index: next, Group: index, Name: fmt.Sprintf("Record %d", next)}
		counter = []byte(strconv.FormatUint(next+1, 10))
	} else {
		row, err = s.metadata(ctx, index)
		if err != nil {
			return fail(err)
		}
		if err = s.authorize(ctx, row.Group); err != nil {
			return fail(err)
		}
	}
	key := strconv.FormatUint(row.Index, 10)
	if path == "/SpineInteractionRecordDetailInfo" {
		raw, err := s.recordData(ctx, row)
		if err != nil {
			return fail(err)
		}
		return code, wire.AppendBytes(nil, 1, raw), true, nil
	}
	if path == "/SpineInteractionRecordDelete" {
		err := s.Store.SaveWithEntries(ctx.State, recordDomain, nil, []stateio.EntryMutation{{Bucket: recordMetadataBucket, Key: key, Delete: true}, {Bucket: recordDataBucket, Key: key, Delete: true}})
		return code, nil, true, err
	}
	var changes []stateio.EntryMutation
	var out []byte
	if path == "/SpineInteractionRecordNameUpdate" {
		name, _, err := wire.Bytes(request, 3)
		if err != nil || !validRecordingName(string(name)) {
			return fail(fmt.Errorf("interaction recording: invalid name"))
		}
		if s.CheckText == nil {
			return fail(fmt.Errorf("interaction recording: missing text policy"))
		}
		if err = s.CheckText(ctx, "spine_record_name", string(name)); err != nil {
			return fail(err)
		}
		row.Name = string(name)
	} else {
		data, _, err := wire.Bytes(request, 3)
		if err != nil {
			return fail(err)
		}
		if err = s.validateRecording(row.Group, data); err != nil {
			return fail(err)
		}
		capability, err := util.RandomBase64URL(32)
		if err != nil {
			return fail(err)
		}
		row.Capability = capability
		digest := sha256.Sum256(data)
		row.Digest = hex.EncodeToString(digest[:])
		url, err := s.url(ctx, row)
		if err != nil {
			return fail(err)
		}
		if path == "/SpineInteractionRecordSave" {
			out = wire.AppendVarint(nil, 1, row.Index)
			out = wire.AppendBytes(out, 2, []byte(row.Name))
			out = wire.AppendBytes(out, 3, []byte(url))
			changes = append(changes, stateio.EntryMutation{Bucket: "spine_record_counter", Key: "next", Payload: counter})
		} else {
			out = wire.AppendBytes(nil, 1, []byte(url))
		}
		changes = append(changes, stateio.EntryMutation{Bucket: recordDataBucket, Key: key, Payload: data})
	}
	raw, err := json.Marshal(row)
	if err != nil {
		return fail(err)
	}
	changes = append(changes, stateio.EntryMutation{Bucket: recordMetadataBucket, Key: key, Payload: raw})
	if err = s.Store.SaveWithEntries(ctx.State, recordDomain, nil, changes); err != nil {
		return fail(err)
	}
	return code, out, true, nil
}

func validRecordingName(name string) bool {
	if !utf8.ValidString(name) || strings.Trim(name, " ") == "" {
		return false
	}
	count := 0
	for _, c := range name {
		allowed := c == ' ' || c >= '0' && c <= '9' || c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= 0xac00 && c <= 0xd7a3 || c >= 0x3041 && c <= 0x30fe || c >= 0x4e00 && c <= 0x9fd5 || c >= 0x180 && c <= 0x24f
		if !allowed {
			return false
		}
		count++
		if c >= 0xac00 && c <= 0xd7a3 || c >= 0x4e00 && c <= 0x9fff || c >= 0x3040 && c <= 0x30fc {
			count++
		}
	}
	return count >= 4 && count <= 20
}

type recordingReader struct{ *bytes.Reader }

func (r recordingReader) integer() (int64, error) {
	var n uint32
	for shift := uint(0); shift < 35; shift += 7 {
		b, err := r.ReadByte()
		if err != nil {
			return 0, err
		}
		if shift == 28 && b > 15 {
			return 0, fmt.Errorf("interaction recording: integer overflow")
		}
		n |= uint32(b&127) << shift
		if b&128 == 0 {
			return int64(int32(n>>1) ^ -int32(n&1)), nil
		}
	}
	return 0, fmt.Errorf("interaction recording: invalid integer")
}

func (s InteractionRecordService) validateRecording(group uint64, data []byte) error {
	invalid := func() error { return fmt.Errorf("interaction recording: invalid native event data") }
	if len(data) < 6 || len(data) > 4<<20 || data[4] != 1 || s.Design.MaxRecordTime == 0 || s.Design.MaxRecordInteractions == 0 {
		return invalid()
	}
	compressed := bytes.NewReader(data[5:])
	gz, err := gzip.NewReader(compressed)
	if err != nil {
		return invalid()
	}
	gz.Multistream(false)
	raw, err := io.ReadAll(io.LimitReader(gz, (16<<20)+1))
	closeErr := gz.Close()
	if err != nil || closeErr != nil || len(raw) > 16<<20 || compressed.Len() != 0 || crc32.ChecksumIEEE(raw) != binary.LittleEndian.Uint32(data[:4]) || len(raw) < 4 {
		return invalid()
	}
	count := uint64(binary.LittleEndian.Uint32(raw[:4]))
	if count == 0 || count > 100000 {
		return invalid()
	}
	r := recordingReader{bytes.NewReader(raw[4:])}
	var timeMS, interactions, dragCount uint64
	limitMS := s.Design.MaxRecordTime * 1000
	for range count {
		flags, err := r.ReadByte()
		if err != nil || flags&^byte(31) != 0 || (flags&7 != 1 && flags&7 != 2 && flags&7 != 4) {
			return invalid()
		}
		delta, e1 := r.integer()
		section, e2 := r.integer()
		point, e3 := r.integer()
		tool, e4 := r.integer()
		if e1 != nil || e2 != nil || e3 != nil || e4 != nil || delta < 0 || section <= 0 || point <= 0 || tool < 0 {
			return invalid()
		}
		if _, known := s.Design.Points[gamedata.InteractionPointKey{group, uint64(section), uint64(point)}]; !known || !s.Design.RecordTools[uint64(tool)] {
			return invalid()
		}
		timeMS += uint64(delta)
		if timeMS > limitMS {
			return invalid()
		}
		if flags&5 != 0 {
			interactions++
			if interactions > s.Design.MaxRecordInteractions {
				return invalid()
			}
		}
		if flags&4 != 0 {
			n, err := r.integer()
			if err != nil || n < 0 || n > 100000 {
				return invalid()
			}
			dragCount += uint64(n)
			if dragCount > 100000 {
				return invalid()
			}
			dragTime := timeMS
			for j := range n {
				kind, e1 := r.ReadByte()
				overlap, e2 := r.ReadByte()
				dt, e3 := r.integer()
				_, e4 := r.integer()
				_, e5 := r.integer()
				if e1 != nil || e2 != nil || e3 != nil || e4 != nil || e5 != nil || kind > 1 || overlap > 1 || dt < 0 || kind == 1 && j != n-1 {
					return invalid()
				}
				dragTime += uint64(dt)
				if dragTime > limitMS {
					return invalid()
				}
			}
		}
		if flags&8 != 0 {
			var scores [2]float32
			if binary.Read(r, binary.LittleEndian, &scores) != nil {
				return invalid()
			}
			for _, score := range scores {
				if math.IsNaN(float64(score)) || math.IsInf(float64(score), 0) {
					return invalid()
				}
			}
		}
	}
	if r.Len() != 0 {
		return invalid()
	}
	return nil
}
