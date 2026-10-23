package update

import (
	"bd2server/internal/common/util"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"

	clientconfig "bd2server/internal/client/config"
	"bd2server/internal/client/download"
	"bd2server/internal/client/setup"
	commonconfig "bd2server/internal/common/config"
	"bd2server/internal/common/release"
	"bd2server/internal/common/versionconfig"
)

const gameReceiptFile = ".bd2-update-receipt.json"

type gameReceipt struct {
	Fingerprint         string             `json:"fingerprint"`
	ResourceFingerprint string             `json:"resource_fingerprint"`
	GameVersion         string             `json:"game_version"`
	BundleVersion       string             `json:"bundle_version"`
	GameDataVersion     string             `json:"game_data_version"`
	Resources           string             `json:"resources"`
	Files               []verifiedGameFile `json:"files,omitempty"`
}

type verifiedGameFile struct {
	Path     string `json:"path"`
	Size     int64  `json:"size"`
	Modified int64  `json:"modified"`
	SHA256   string `json:"sha256"`
}

func readGameReceipt(game string) (gameReceipt, error) {
	return commonconfig.Load(filepath.Join(game, gameReceiptFile), gameReceipt{}, commonconfig.DecodeOptions{RejectTrailing: true, RejectNull: true})
}

func gameReceiptMatches(game, id string) bool {
	receipt, err := readGameReceipt(game)
	if err != nil || receipt.Fingerprint != id {
		return false
	}
	for _, file := range receipt.Files {
		info, err := os.Stat(file.Path)
		if err != nil || !info.Mode().IsRegular() || info.Size() != file.Size {
			return false
		}
		if info.ModTime().UnixNano() != file.Modified && !release.Matches(file.Path, release.File{Size: file.Size, SHA256: file.SHA256}) {
			return false
		}
	}
	return true
}

func (m *Manager) Versions(game string) versionconfig.Config {
	v := m.options.Versions
	if receipt, err := readGameReceipt(game); err == nil && len(receipt.Files) > 0 && gameReceiptMatches(game, receipt.Fingerprint) {
		v.GameVersion, v.BundleVersion, v.GameDataVersion = receipt.GameVersion, receipt.BundleVersion, receipt.GameDataVersion
	}
	return v
}

func (m *Manager) Runtime(game string) *clientconfig.Runtime {
	if m.options.Development {
		return m.developmentRuntime()
	}
	v := m.Versions(game)
	runtime := &clientconfig.Runtime{ClientVersion: v.ClientVersion, GameVersion: v.GameVersion, BundleVersion: v.BundleVersion, GameDataVersion: v.GameDataVersion}
	m.mu.Lock()
	fp := m.fingerprint
	m.mu.Unlock()
	if receipt, err := readGameReceipt(game); err == nil && fp != nil && gameReceiptMatches(game, fp.ID) {
		runtime.UpdateFingerprint = fp.ID
		if receipt.ResourceFingerprint == fp.GameFingerprint && len(receipt.Files) > 0 {
			runtime.ResourceFingerprint = receipt.ResourceFingerprint
		}
	}
	return runtime
}

func (m *Manager) configureGame(game string) error {
	settings, err := clientconfig.Load(game)
	if err != nil {
		return err
	}
	settings.Runtime = m.Runtime(game)
	_, err = clientconfig.Save(game, settings)
	return err
}

// developmentRuntime describes the game prepared from the repository's own
// versions.json and UPDATE_FINGERPRINT.json instead of a launcher receipt.
func (m *Manager) developmentRuntime() *clientconfig.Runtime {
	v := m.options.Versions
	runtime := &clientconfig.Runtime{ClientVersion: v.ClientVersion, GameVersion: v.GameVersion, BundleVersion: v.BundleVersion, GameDataVersion: v.GameDataVersion}
	if m.project != nil {
		runtime.UpdateFingerprint, runtime.ResourceFingerprint = m.project.ID, m.project.GameFingerprint
	}
	return runtime
}

// prepareDevelopmentGame never downloads: development games are prepared by the
// project tools, so a mismatch is reported instead of reinstalled.
func (m *Manager) prepareDevelopmentGame(game string) error {
	if m.projectErr != nil {
		return fmt.Errorf("project update fingerprint: %w", m.projectErr)
	}
	v := m.options.Versions
	if m.project != nil {
		if err := m.project.MatchesVersions(v); err != nil {
			return fmt.Errorf("project %s does not match versions.json: %w", release.FingerprintFileName, err)
		}
	}
	m.mu.Lock()
	server := m.fingerprint
	m.mu.Unlock()
	if server != nil && (m.project == nil || server.ID != m.project.ID) {
		return fmt.Errorf("server requires update %s for game %s; update the project's %s before launching", server.ID, gameVersion(server), release.FingerprintFileName)
	}
	if _, err := setup.Inspect(game, v); err != nil {
		return fmt.Errorf("development game does not match versions.json: %w", err)
	}
	settings, err := clientconfig.Load(game)
	if err != nil {
		return err
	}
	if _, err := setup.Patch(game); err != nil {
		return fmt.Errorf("apply development client patch: %w", err)
	}
	if settings.CDNMode == clientconfig.CDNLocal {
		if err := setup.InstallLocalCatalog(game, settings.LocalResourceDirectory, v.BundleVersion); err != nil {
			return fmt.Errorf("prepare development resource catalog: %w", err)
		}
	}
	return nil
}

func (m *Manager) ensureGame(ctx context.Context, game string) error {
	if m.options.Development {
		return m.prepareDevelopmentGame(game)
	}
	m.mu.Lock()
	fp := m.fingerprint
	fingerprintErr := m.fingerprintErr
	m.mu.Unlock()
	if fingerprintErr != nil {
		return fingerprintErr
	}
	if fp == nil {
		m.mu.Lock()
		delete(m.gamePending, game)
		if len(m.gamePending) == 0 {
			m.status.Game = nil
		}
		m.mu.Unlock()
		return nil
	}
	previous, previousErr := readGameReceipt(game)
	if gameReceiptMatches(game, fp.ID) && (!fp.RequireGameUpdate || previous.ResourceFingerprint == fp.GameFingerprint && len(previous.Files) > 0) {
		return nil
	}
	if fp.RequireClientUpdate && clientUpdateRequired(m.options.Versions.ClientVersion, fp.ClientVersion) {
		return fmt.Errorf("update the launcher to %s before updating the game", fp.ClientVersion)
	}
	m.options.Logger.Info("game installation does not match the server requirement", "game", game, "requirement", fp.ID, "game_version", gameVersion(fp))
	reuseResources := previousErr == nil && previous.ResourceFingerprint == fp.GameFingerprint && fp.GameFingerprint != "" && len(previous.Files) > 0 && gameReceiptMatches(game, previous.Fingerprint)
	installGame := fp.RequireGameUpdate && !reuseResources
	if installGame {
		if fp.Game == nil {
			return errors.New("required game update has no signed download inventory")
		}
		m.mu.Lock()
		m.status.Game = &Offer{Version: fp.Game.GameVersion}
		m.mu.Unlock()
		for _, instance := range m.instances() {
			if running, err := m.gameRunning(instance); err != nil {
				return err
			} else if running {
				m.mu.Lock()
				m.gamePending[game] = true
				m.mu.Unlock()
				return errors.New("close all game instances to install the required game and resource update")
			}
		}
		if running, err := m.gameRunning(game); err != nil {
			return err
		} else if running {
			m.mu.Lock()
			m.gamePending[game] = true
			m.mu.Unlock()
			return errors.New("close the game to install the required game and resource update")
		}
	}
	v := m.Versions(game)
	resources := ""
	if installGame {
		settings, err := clientconfig.Load(game)
		if err != nil {
			return fmt.Errorf("load game settings before updating: %w", err)
		}
		resources = settings.LocalResourceDirectory
		if resources == "" {
			resources = filepath.Join(m.options.Directory, "resources")
		}
		m.options.Logger.Info("downloading required game and resources", "game", game, "resources", resources, "game_version", fp.Game.GameVersion, "bundle_version", fp.Game.BundleVersion, "game_data_version", fp.Game.GameDataVersion)
		installed, err := download.Client(ctx, download.Options{PackageDir: m.options.Directory, GameDir: game, ResourceDir: resources, Workers: 8, Pinned: fp.Game, Progress: m.ReportDownload(DownloadGame)}, io.Discard)
		m.EndDownload()
		if err != nil {
			return err
		}
		v.GameVersion, v.BundleVersion, v.GameDataVersion = installed.Versions.GameVersion, installed.Versions.BundleVersion, installed.Versions.GameDataVersion
		resources = installed.ResourceDir
		if _, err := setup.SaveSettings(game, settings); err != nil {
			return fmt.Errorf("restore game connection settings: %w", err)
		}
		if _, err := setup.Patch(game); err != nil {
			return fmt.Errorf("restore game connection patch: %w", err)
		}
		if err := setup.InstallLocalCatalog(game, resources, v.BundleVersion); err != nil {
			return fmt.Errorf("prepare verified game resource catalog: %w", err)
		}
	}
	receipt := gameReceipt{Fingerprint: fp.ID, GameVersion: v.GameVersion, BundleVersion: v.BundleVersion, GameDataVersion: v.GameDataVersion, Resources: resources}
	if installGame {
		receipt.ResourceFingerprint = fp.GameFingerprint
		for _, pin := range fp.Game.ResourceFiles {
			path := filepath.Join(resources, filepath.FromSlash(pin.Path))
			info, err := os.Stat(path)
			if err != nil {
				return err
			}
			if !info.Mode().IsRegular() || info.Size() != pin.Size {
				return fmt.Errorf("required resource was not installed: %s", pin.Path)
			}
			receipt.Files = append(receipt.Files, verifiedGameFile{Path: path, Size: pin.Size, Modified: info.ModTime().UnixNano(), SHA256: pin.SHA256})
			prefix := "ServerData/" + fp.Game.Platform + "/HD/" + fp.Game.BundleVersion + "/"
			if relative, ok := strings.CutPrefix(pin.Path, prefix); ok && strings.EqualFold(filepath.Ext(relative), ".bundle") {
				streamed := filepath.Join(game, "BrownDust II_Data", "StreamingAssets", "aa", filepath.FromSlash(relative))
				streamedInfo, err := os.Stat(streamed)
				if err != nil {
					return err
				}
				receipt.Files = append(receipt.Files, verifiedGameFile{Path: streamed, Size: pin.Size, Modified: streamedInfo.ModTime().UnixNano(), SHA256: pin.SHA256})
			}
		}
		for _, relative := range []string{"BrownDust II.exe", "UnityPlayer.dll", "BrownDust II_Data/globalgamemanagers", "BrownDust II_Data/Managed/Assembly-CSharp.dll", "BrownDust II_Data/resources.assets", "BrownDust II_Data/StreamingAssets/aa/catalog.json", "BrownDust II_Data/StreamingAssets/aa/.bd2-local-bundles.json"} {
			path := filepath.Join(game, filepath.FromSlash(relative))
			info, err := os.Stat(path)
			if err != nil {
				return err
			}
			digest, err := util.SHA256File(path)
			if err != nil {
				return err
			}
			receipt.Files = append(receipt.Files, verifiedGameFile{Path: path, Size: info.Size(), Modified: info.ModTime().UnixNano(), SHA256: digest})
		}
	} else if previousErr == nil {
		receipt.Files, receipt.Resources, receipt.ResourceFingerprint = previous.Files, previous.Resources, previous.ResourceFingerprint
	}
	if err := commonconfig.Save(filepath.Join(game, gameReceiptFile), receipt, commonconfig.WriteOptions{FileMode: 0644}); err != nil {
		return err
	}
	m.options.Logger.Info("game installation recorded for the server requirement", "game", game, "requirement", fp.ID, "downloaded", installGame, "reused_resources", reuseResources, "verified_files", len(receipt.Files))
	m.mu.Lock()
	delete(m.gamePending, game)
	if len(m.gamePending) == 0 {
		m.status.Game = nil
	}
	m.mu.Unlock()
	return nil
}

func (m *Manager) CommitGame(ctx context.Context) error {
	if !m.begin("installing") {
		return operationBusy()
	}
	defer m.end()
	var first error
	for _, game := range m.instances() {
		settings, err := clientconfig.Load(game)
		if err != nil {
			if first == nil {
				first = err
			}
			continue
		}
		if err := m.CheckServer(ctx, m.options.Client, settings.ServerOrigin); err != nil {
			if first == nil {
				first = err
			}
			continue
		}
		err = m.ensureGame(ctx, game)
		if err == nil {
			err = m.configureGame(game)
		}
		if err != nil && first == nil {
			first = err
		}
	}
	m.mu.Lock()
	if first != nil {
		m.status.Error = first.Error()
	} else {
		m.status.Error = ""
	}
	m.mu.Unlock()
	return first
}

func (m *Manager) installGameUpdates(ctx context.Context) {
	m.mu.Lock()
	waiting := len(m.gamePending) != 0
	m.mu.Unlock()
	if waiting {
		_ = m.CommitGame(ctx)
	}
}

// PrepareGame resolves the server's signed requirement before inspecting a
// possibly obsolete game installation with the new release's versions.
func (m *Manager) PrepareGame(ctx context.Context, game, origin string) error {
	if err := m.acquire(ctx, "preparing"); err != nil {
		return err
	}
	defer m.end()
	if err := m.CheckServer(ctx, m.options.Client, origin); err != nil {
		return err
	}
	if err := m.ensureGame(ctx, game); err != nil {
		return err
	}
	return m.configureGame(game)
}

func (m *Manager) loadPackagedFingerprint() {
	fp, err := release.LoadFingerprint(filepath.Join(m.options.Directory, release.FingerprintFileName), m.keys)
	if err == nil {
		m.fingerprint = fp
	} else if !errors.Is(err, os.ErrNotExist) {
		m.status.Error = err.Error()
		m.fingerprintErr = err
	}
}

func (m *Manager) fetchServerFingerprint(ctx context.Context, client *http.Client, origin, id string) error {
	address, err := url.JoinPath(origin, "client", "update-fingerprint")
	if err != nil {
		return err
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, address, nil)
	if err != nil {
		return err
	}
	response, err := client.Do(request)
	if err != nil {
		return fmt.Errorf("fetch server update fingerprint: %w", err)
	}
	defer func() { _ = response.Body.Close() }()
	if response.StatusCode != http.StatusOK {
		return fmt.Errorf("fetch server update fingerprint: %w", util.ResponseError(response))
	}
	raw, err := io.ReadAll(io.LimitReader(response.Body, (32<<20)+1))
	if err != nil {
		return err
	}
	if len(raw) > 32<<20 {
		return errors.New("server update fingerprint exceeds size limit")
	}
	fp, err := release.VerifyFingerprint(raw, m.keys)
	if err != nil {
		return err
	}
	if fp.ID != id {
		return errors.New("server update requirement changed; retry after the update finishes")
	}
	m.mu.Lock()
	m.fingerprint = &fp
	m.fingerprintErr = nil
	m.mu.Unlock()
	return nil
}

func clientUpdateRequired(current, target string) bool {
	newer, err := release.Newer(current, target)
	return err != nil || newer
}

func (m *Manager) clearServerRequirement() {
	m.mu.Lock()
	m.fingerprint = nil
	m.mu.Unlock()
}

func gameVersion(fp *release.Fingerprint) string {
	if fp.Game == nil {
		return ""
	}
	return fp.Game.GameVersion
}
