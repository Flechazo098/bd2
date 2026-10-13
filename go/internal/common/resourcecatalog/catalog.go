// Package resourcecatalog reads Addressables catalog IDs and resolves CDN bundles.
package resourcecatalog

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"path"
	"sort"
	"strings"
)

const RemotePrefix = "{BDNetwork.CdnInfo.Info}"

func Decode(raw []byte) (map[string]json.RawMessage, []string, error) {
	var document map[string]json.RawMessage
	decoder := json.NewDecoder(bytes.NewReader(raw))
	if err := decoder.Decode(&document); err != nil {
		return nil, nil, fmt.Errorf("resource catalog: decode: %w", err)
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return nil, nil, errors.New("resource catalog: must contain exactly one JSON object")
	}
	var ids []string
	if err := json.Unmarshal(document["m_InternalIds"], &ids); err != nil || len(ids) == 0 {
		return nil, nil, errors.New("resource catalog: no valid internal IDs")
	}
	return document, ids, nil
}

func BundlePath(internalID string) (string, error) {
	parts := strings.Split(strings.ReplaceAll(internalID, "\\", "/"), "/")
	if len(parts) < 5 || parts[0] != RemotePrefix {
		return "", fmt.Errorf("resource catalog: invalid CDN path %q", internalID)
	}
	parts = parts[4:]
	for _, part := range parts {
		if part == "" || part == "." || part == ".." || strings.ContainsAny(part, `:{}`) {
			return "", fmt.Errorf("resource catalog: unsafe bundle path %q", internalID)
		}
	}
	relative := strings.Join(parts, "/")
	if !strings.EqualFold(path.Ext(relative), ".bundle") {
		return "", fmt.Errorf("resource catalog: CDN path is not a bundle %q", internalID)
	}
	return relative, nil
}

func BundlePaths(raw []byte) ([]string, error) {
	_, ids, err := Decode(raw)
	if err != nil {
		return nil, err
	}
	unique := make(map[string]struct{})
	for _, id := range ids {
		if !strings.HasPrefix(id, RemotePrefix) {
			continue
		}
		relative, err := BundlePath(id)
		if err != nil {
			return nil, err
		}
		unique[relative] = struct{}{}
	}
	if len(unique) == 0 {
		return nil, errors.New("resource catalog: no CDN bundle paths")
	}
	paths := make([]string, 0, len(unique))
	for relative := range unique {
		paths = append(paths, relative)
	}
	sort.Strings(paths)
	return paths, nil
}
