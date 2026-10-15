// Package update keeps the launcher and its game plugins current. It checks
// the signed release manifest in the background, downloads only the files
// whose digests differ, and installs them without touching anything running:
// the launcher switches through a restart, and a plugin set is installed into
// each game instance only while that instance is closed.
package update

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"maps"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"

	clientlayout "bd2server/internal/client/layout"
	"bd2server/internal/client/plugin"
	"bd2server/internal/client/setup"
	commonconfig "bd2server/internal/common/config"
	"bd2server/internal/common/i18n"
	"bd2server/internal/common/release"
	"bd2server/internal/common/versionconfig"
)

// StoreFile describes the launcher's plugin store, plugins/plugins.json.
const StoreFile = "plugins.json"

// ManifestEnvironment overrides the built-in manifest URL.
const ManifestEnvironment = "BD2_UPDATE_MANIFEST_URL"

type Store struct {
	Version string       `json:"version"`
	Files   []StoredFile `json:"files"`
}

type StoredFile struct {
	ID      string `json:"id,omitempty"`
	Name    string `json:"name"`
	Version string `json:"version,omitempty"`
	SHA256  string `json:"sha256"`
}

type Offer struct {
	Version string    `json:"version"`
	Date    string    `json:"date,omitempty"`
	Notes   i18n.Text `json:"notes,omitempty"`
	Bytes   int64     `json:"bytes"`
	Ready   bool      `json:"ready"`
}

type Status struct {
	Enabled  bool      `json:"enabled"`
	Checked  time.Time `json:"checked,omitzero"`
	Error    string    `json:"error,omitempty"`
	Launcher *Offer    `json:"launcher,omitempty"`
	Plugins  *Offer    `json:"plugins,omitempty"`
	Game     *Offer    `json:"game,omitempty"`
	Busy     string    `json:"busy,omitempty"`
	Done     int64     `json:"done,omitempty"`
	Total    int64     `json:"total,omitempty"`
	// Waiting lists game directories whose new plugins install on exit.
	Waiting []string `json:"waiting,omitempty"`
	Message *Message `json:"message,omitempty"`
}

// Message is a translation key with arguments, rendered by the launcher UI.
type Message struct {
	Key  string   `json:"key"`
	Args []string `json:"args,omitempty"`
}

func message(key string, args ...string) *Message { return &Message{Key: key, Args: args} }

type Options struct {
	Versions  versionconfig.Config
	Directory string
	Logger    *slog.Logger
	Client    *http.Client
	// Games returns the main game directory the launcher manages.
	Games func() string
}

type Manager struct {
	options     Options
	manifestURL string
	keys        []release.TrustedKey

	operation      sync.Mutex
	envelope       []byte
	mu             sync.Mutex
	status         Status
	manifest       *release.Manifest
	fingerprint    *release.Fingerprint
	fingerprintErr error
	gamePending    map[string]bool
	pending        map[string]bool
}

func New(options Options) *Manager {
	if options.Logger == nil {
		options.Logger = slog.Default()
	}
	address := strings.TrimSpace(os.Getenv(ManifestEnvironment))
	if address == "" {
		address = release.BuiltinChannel().ManifestURL
	}
	m := &Manager{options: options, manifestURL: address, keys: release.BuiltinChannel().TrustedKeys, pending: map[string]bool{}, gamePending: map[string]bool{}}
	m.status.Enabled = address != ""
	m.loadPackagedFingerprint()
	if result := TakeResult(options.Directory); result != nil {
		if result.OK {
			m.status.Message = message("update.launcher.switched", result.From, result.To)
		} else {
			m.status.Message = message("update.launcher.switch_failed", result.To, result.Message, result.From)
		}
		options.Logger.Info("launcher switch result", "from", result.From, "to", result.To, "ok", result.OK, "message", result.Message)
	}
	return m
}

func (m *Manager) Status() Status {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := m.status
	cloneOffer := func(o *Offer) *Offer {
		if o == nil {
			return nil
		}
		out := *o
		out.Notes = maps.Clone(o.Notes)
		return &out
	}
	out.Launcher, out.Plugins, out.Game = cloneOffer(out.Launcher), cloneOffer(out.Plugins), cloneOffer(out.Game)
	if out.Message != nil {
		message := *out.Message
		message.Args = slices.Clone(message.Args)
		out.Message = &message
	}
	out.Waiting = nil
	for game := range m.pending {
		out.Waiting = append(out.Waiting, game)
	}
	for game := range m.gamePending {
		if !m.pending[game] {
			out.Waiting = append(out.Waiting, game)
		}
	}
	slices.Sort(out.Waiting)
	return out
}

// Busy reports a running download or installation; launches wait for it.
func (m *Manager) Busy() bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.status.Busy != ""
}

func (m *Manager) setBusy(value string) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	if value != "" && m.status.Busy != "" {
		return false
	}
	m.status.Busy, m.status.Done, m.status.Total = value, 0, 0
	return true
}

func operationBusy() error {
	return errors.New("Another update operation is in progress") //nolint:staticcheck // ST1005: shown to players
}

func (m *Manager) begin(busy string) bool {
	if !m.operation.TryLock() {
		return false
	}
	m.setBusy(busy)
	return true
}

func (m *Manager) end() { m.setBusy(""); m.operation.Unlock() }

// Run checks shortly after start and then every six hours, downloads what
// is newer, installs a downloaded plugin set wherever no game runs, and
// installs the rest as each game instance exits.
func (m *Manager) Run(ctx context.Context) {
	if !m.status.Enabled {
		return
	}
	check := time.NewTimer(3 * time.Second)
	defer check.Stop()
	watch := time.NewTicker(5 * time.Second)
	defer watch.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-check.C:
			m.refresh(ctx)
			check.Reset(6 * time.Hour)
		case <-watch.C:
			m.installGameUpdates(ctx)
			m.installWaiting()
		}
	}
}

func (m *Manager) refresh(ctx context.Context) {
	if err := m.Check(ctx); err != nil {
		m.options.Logger.Warn("update check failed", "error", err)
		return
	}
	if err := m.Download(ctx); err != nil {
		m.options.Logger.Warn("update download failed", "error", err)
		return
	}
	if err := m.CommitGame(ctx); err != nil {
		m.options.Logger.Warn("game resources not installed", "error", err)
	}
	if err := m.CommitPlugins(); err != nil {
		m.options.Logger.Warn("plugin update not installed", "error", err)
	}
}

// Check fetches the manifest and works out what this launcher lacks.
func (m *Manager) Check(ctx context.Context) error {
	if !m.begin("checking") {
		return operationBusy()
	}
	defer m.end()
	fetchCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	manifest, envelope, err := release.FetchManifest(fetchCtx, m.options.Client, m.manifestURL, m.keys)
	m.mu.Lock()
	defer m.mu.Unlock()
	m.status.Checked = time.Now().UTC()
	if err != nil {
		m.status.Error = err.Error()
		return err
	}
	m.status.Error = ""
	m.manifest = &manifest
	if manifest.Fingerprint != nil {
		m.fingerprint = manifest.Fingerprint
		m.fingerprintErr = nil
	}
	m.status.Game = nil
	if fp := manifest.Fingerprint; fp != nil && fp.RequireGameUpdate && fp.Game != nil {
		for _, game := range m.instances() {
			if !gameReceiptMatches(game, fp.ID) {
				m.gamePending[game] = true
				m.status.Game = &Offer{Version: fp.Game.GameVersion}
			}
		}
	}
	m.envelope = envelope
	m.status.Launcher, m.status.Plugins = nil, nil
	if c := manifest.Launcher; c != nil && newer(m.options.Versions.ClientVersion, c.Version) {
		if files := m.missing(c.FilesFor(release.Platform()), m.options.Directory); len(files) != 0 {
			m.status.Launcher = offer(c, files, m.launcherStaging(c.Version))
		}
	}
	if c := manifest.Plugins; c != nil && !newer(c.Version, m.storeVersion()) {
		if files := m.missing(c.Files, filepath.Join(m.options.Directory, "plugins")); len(files) != 0 || !m.storeMatches(c) {
			m.status.Plugins = offer(c, files, m.pluginStaging(c.Version))
		}
	}
	return nil
}

func newer(current, candidate string) bool {
	ok, err := release.Newer(current, candidate)
	return err == nil && ok
}

func offer(c *release.Component, files []release.File, staging string) *Offer {
	o := &Offer{Version: c.Version, Date: c.Date, Notes: c.Notes, Ready: true}
	for _, f := range files {
		o.Bytes += f.Size
		if !release.Matches(filepath.Join(staging, f.Name), f) {
			o.Ready = false
		}
	}
	return o
}

func (m *Manager) missing(files []release.File, directory string) []release.File {
	var out []release.File
	for _, f := range files {
		if !release.Matches(filepath.Join(directory, f.Name), f) {
			out = append(out, f)
		}
	}
	return out
}

func (m *Manager) launcherStaging(version string) string {
	return filepath.Join(m.options.Directory, ".update", "launcher", version)
}

func (m *Manager) pluginStaging(version string) string {
	return filepath.Join(m.options.Directory, ".update", "plugins", version)
}

// ReadStore returns the launcher's plugin store description.
func ReadStore(directory string) (Store, error) {
	var store Store
	raw, err := os.ReadFile(filepath.Join(directory, "plugins", StoreFile))
	if err != nil {
		return store, err
	}
	return store, json.Unmarshal(raw, &store)
}

func (m *Manager) storeVersion() string {
	store, err := ReadStore(m.options.Directory)
	if err != nil || store.Version == "" {
		return m.options.Versions.ClientVersion
	}
	return store.Version
}

func (m *Manager) storeMatches(component *release.Component) bool {
	store, err := ReadStore(m.options.Directory)
	if err != nil || store.Version != component.Version || len(store.Files) != len(component.Files) {
		return false
	}
	for i, f := range component.Files {
		have := store.Files[i]
		if have.Name != f.Name || have.SHA256 != f.SHA256 || have.ID != f.ID || have.Version != f.Version {
			return false
		}
	}
	return true
}

// Download fetches every offered file that is not already staged.
func (m *Manager) Download(ctx context.Context) error {
	if !m.begin("downloading") {
		return operationBusy()
	}
	defer m.end()
	m.mu.Lock()
	manifest := m.manifest
	launcher, plugins := m.status.Launcher, m.status.Plugins
	m.mu.Unlock()
	if manifest == nil || (launcher == nil && plugins == nil) {
		return nil
	}

	type job struct {
		files   []release.File
		staging string
		offer   *Offer
	}
	var jobs []job
	var total int64
	if launcher != nil && !launcher.Ready {
		files := m.missing(manifest.Launcher.FilesFor(release.Platform()), m.options.Directory)
		jobs = append(jobs, job{files, m.launcherStaging(manifest.Launcher.Version), launcher})
		total += launcher.Bytes
	}
	if plugins != nil && !plugins.Ready {
		files := m.missing(manifest.Plugins.Files, filepath.Join(m.options.Directory, "plugins"))
		jobs = append(jobs, job{files, m.pluginStaging(manifest.Plugins.Version), plugins})
		total += plugins.Bytes
	}
	m.mu.Lock()
	m.status.Total = total
	m.mu.Unlock()
	var finished int64
	for _, j := range jobs {
		for _, f := range j.files {
			_, err := release.Download(ctx, m.options.Client, m.manifestURL, f, j.staging, func(_ string, done, _ int64) {
				m.mu.Lock()
				m.status.Done = finished + done
				m.mu.Unlock()
			})
			if err != nil {
				return err
			}
			finished += f.Size
		}
		m.mu.Lock()
		j.offer.Ready = true
		m.mu.Unlock()
	}
	return nil
}

// CommitPlugins moves a downloaded plugin set into the launcher's store and
// installs it into every closed game instance; running ones wait.
func (m *Manager) CommitPlugins() error {
	if !m.begin("installing") {
		return operationBusy()
	}
	defer m.end()
	m.mu.Lock()
	manifest, plugins := m.manifest, m.status.Plugins
	m.mu.Unlock()
	if manifest == nil || plugins == nil || !plugins.Ready {
		return nil
	}

	if err := m.replaceStore(manifest.Plugins); err != nil {
		return err
	}
	m.mu.Lock()
	m.status.Plugins = nil
	m.mu.Unlock()
	_ = os.RemoveAll(filepath.Join(m.options.Directory, ".update", "plugins"))
	var waiting []string
	for _, game := range m.instances() {
		if running, err := m.gameRunning(game); err != nil || running {
			waiting = append(waiting, game)
			m.mu.Lock()
			m.pending[game] = true
			m.mu.Unlock()
			continue
		}
		if err := m.ensurePlugins(game); err != nil {
			m.mu.Lock()
			m.pending[game] = true
			m.mu.Unlock()
			waiting = append(waiting, game)
			m.options.Logger.Warn("plugin set not installed", "game", game, "error", err)
		}
	}
	result := message("update.plugins.updated", manifest.Plugins.Version)
	if len(waiting) != 0 {
		result = message("update.plugins.updated_waiting", manifest.Plugins.Version)
	}
	m.mu.Lock()
	m.status.Message = result
	m.mu.Unlock()
	m.options.Logger.Info("plugin store updated", "version", manifest.Plugins.Version, "waiting", waiting)
	return nil
}

// replaceStore builds the complete new store beside the old one and swaps
// the directories, so the store always holds one consistent set.
func (m *Manager) replaceStore(c *release.Component) error {
	current := filepath.Join(m.options.Directory, "plugins")
	next := current + ".new"
	previous := current + ".previous"
	if err := os.RemoveAll(next); err != nil {
		return err
	}
	if err := os.MkdirAll(next, 0o755); err != nil {
		return err
	}
	defer func() { _ = os.RemoveAll(next) }()
	store := Store{Version: c.Version}
	for _, f := range c.Files {
		source := filepath.Join(current, f.Name)
		if !release.Matches(source, f) {
			source = filepath.Join(m.pluginStaging(c.Version), f.Name)
			if !release.Matches(source, f) {
				return fmt.Errorf("plugin %s was not downloaded completely", f.Name)
			}
		}
		if err := copyFile(source, filepath.Join(next, f.Name)); err != nil {
			return err
		}
		if !release.Matches(filepath.Join(next, f.Name), f) {
			return fmt.Errorf("plugin %s changed while copying", f.Name)
		}
		store.Files = append(store.Files, StoredFile{ID: f.ID, Name: f.Name, Version: f.Version, SHA256: f.SHA256})
	}
	if err := commonconfig.Save(filepath.Join(next, StoreFile), store, commonconfig.WriteOptions{}); err != nil {
		return err
	}
	if err := os.RemoveAll(previous); err != nil {
		return err
	}
	if err := os.Rename(current, previous); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if err := os.Rename(next, current); err != nil {
		if restoreErr := os.Rename(previous, current); restoreErr != nil && !errors.Is(restoreErr, os.ErrNotExist) {
			return errors.Join(err, fmt.Errorf("restore plugin store: %w", restoreErr))
		}
		return err
	}
	return nil
}

// StoreSources lists the plugin files of the current store set.
func (m *Manager) StoreSources() ([]string, error) {
	m.operation.Lock()
	defer m.operation.Unlock()
	return m.storeSources()
}

func (m *Manager) storeSources() ([]string, error) {
	directory := filepath.Join(m.options.Directory, "plugins")
	store, err := ReadStore(m.options.Directory)
	if err != nil {
		return nil, err
	}
	var sources []string
	if len(store.Files) == 0 {
		return nil, errors.New("plugin store contains no files")
	}
	for _, f := range store.Files {
		if filepath.Base(f.Name) != f.Name || strings.ContainsAny(f.Name, `\:`) || filepath.Ext(f.Name) != ".dll" {
			return nil, fmt.Errorf("invalid stored plugin %q", f.Name)
		}
		source := filepath.Join(directory, f.Name)
		digest, err := release.HashFile(source)
		if err != nil {
			return nil, err
		}
		if digest != f.SHA256 {
			return nil, fmt.Errorf("stored plugin %s failed verification", f.Name)
		}
		sources = append(sources, source)
	}
	return sources, nil
}

// EnsurePlugins installs the store set into a closed game instance when it
// differs from what that instance has.
func (m *Manager) EnsurePlugins(game string) error {
	if !m.begin("installing") {
		return operationBusy()
	}
	defer m.end()
	return m.ensurePlugins(game)
}

func (m *Manager) ensurePlugins(game string) error {
	sources, err := m.storeSources()
	if errors.Is(err, os.ErrNotExist) {
		return nil // development builds install plugins explicitly
	}
	if err != nil {
		return err
	}
	if versions := m.Versions(game); versions.GameVersion != "" {
		store, err := ReadStore(m.options.Directory)
		if err != nil {
			return err
		}
		releaseVersion, err := release.ParseVersion(store.Version)
		if err != nil {
			return err
		}
		if releaseVersion.GameVersion() != versions.GameVersion {
			return errors.New("the downloaded plugins require the game update to finish first")
		}
		if _, err := setup.Inspect(game, versions); err != nil {
			return err
		}
	}
	if installed, err := plugin.Installed(game, sources); err != nil || installed {
		if installed {
			m.mu.Lock()
			delete(m.pending, game)
			m.mu.Unlock()
		}
		return err
	}
	if running, err := m.gameRunning(game); err != nil {
		return err
	} else if running {
		return errors.New("The game is running; its plugins update after it exits") //nolint:staticcheck // ST1005: shown to players
	}
	_, err = plugin.InstallSet(game, sources)
	if err == nil {
		m.mu.Lock()
		delete(m.pending, game)
		m.mu.Unlock()
		m.options.Logger.Info("plugin set installed", "game", game)
	}
	return err
}

func (m *Manager) installWaiting() {
	if !m.begin("installing") {
		return
	}
	defer m.end()
	m.mu.Lock()
	var games []string
	for game := range m.pending {
		games = append(games, game)
	}
	m.mu.Unlock()
	for _, game := range games {
		if running, err := m.gameRunning(game); err != nil || running {
			continue
		}
		if err := m.ensurePlugins(game); err != nil {
			m.options.Logger.Warn("plugin set not installed after game exit", "game", game, "error", err)
			continue
		}
		m.mu.Lock()
		m.status.Message = message("update.plugins.applied_after_exit")
		m.mu.Unlock()
	}
}

func (m *Manager) gameRunning(game string) (bool, error) {
	installation, err := clientlayout.Resolve(game)
	if err != nil {
		return false, err
	}
	running, err := runningExecutables()
	if err != nil {
		return false, err
	}
	return running[executableKey(installation.Executable)], nil
}

// instances lists the main game directory and the second clients prepared
// beside it, which share downloads but are installed separately.
func (m *Manager) instances() []string {
	if m.options.Games == nil {
		return nil
	}
	main := m.options.Games()
	installation, err := clientlayout.Resolve(main)
	if err != nil {
		return nil
	}
	games := []string{main}
	parent := filepath.Join(filepath.Dir(installation.Root), "BD2 Instances")
	entries, _ := os.ReadDir(parent)
	for _, entry := range entries {
		candidate := filepath.Join(parent, entry.Name(), "game")
		if installation.Kind == clientlayout.MacOS {
			candidate = filepath.Join(candidate, filepath.Base(installation.Root))
		}
		if _, err := clientlayout.Resolve(candidate); err == nil {
			games = append(games, candidate)
		}
	}
	return games
}

// ApplyLauncher switches to the downloaded launcher and restarts it. On
// Windows a copy of the launcher performs the switch after quit is called.
func (m *Manager) ApplyLauncher(quit func()) error {
	if !m.begin("restarting") {
		return operationBusy()
	}
	defer m.end()
	m.mu.Lock()
	manifest, launcher := m.manifest, m.status.Launcher
	m.mu.Unlock()
	if manifest == nil || launcher == nil {
		return errors.New("No launcher update is available") //nolint:staticcheck // ST1005: shown to players
	}
	if !launcher.Ready {
		return errors.New("The launcher update has not finished downloading") //nolint:staticcheck // ST1005: shown to players
	}

	staging := m.launcherStaging(launcher.Version)
	current := m.options.Versions.ClientVersion
	if err := m.prepareLauncher(staging, manifest.Launcher); err != nil {
		return err
	}
	if !helperRequired {
		if err := switchLauncher(staging, m.options.Directory, current, launcher.Version, m.keys); err != nil {
			return err
		}
		if err := writeResult(m.options.Directory, Result{From: current, To: launcher.Version, OK: true, At: time.Now().UTC()}); err != nil {
			return err
		}
		if err := startDetached(filepath.Join(m.options.Directory, launcherExecutable())); err != nil {
			return err
		}
		quit()
		return nil
	}
	helper := filepath.Join(m.options.Directory, ".update", "helper", launcherExecutable())
	source, err := os.Executable()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(helper), 0o755); err != nil {
		return err
	}
	if err := copyFile(source, helper); err != nil {
		return fmt.Errorf("prepare launcher updater: %w", err)
	}
	if err := startDetached(helper, HelperFlag, staging, m.options.Directory, fmt.Sprint(os.Getpid()), current, launcher.Version); err != nil {
		return err
	}
	m.options.Logger.Info("launcher updater started; exiting", "from", current, "to", launcher.Version)
	quit()
	return nil
}

// Compatibility is what a server reports at /client/compatibility.
type Compatibility struct {
	ServerVersion  string               `json:"server_version"`
	GameVersion    string               `json:"game_version"`
	MinimumPlugins map[string]string    `json:"minimum_plugins"`
	LoginsClosed   bool                 `json:"logins_closed"`
	Fingerprint    *release.Fingerprint `json:"fingerprint,omitempty"`
	FingerprintURL string               `json:"fingerprint_url,omitempty"`
}

// CheckServer refuses a launch when the server needs newer plugins than the
// store holds. Servers without the endpoint impose no requirement.
func (m *Manager) CheckServer(ctx context.Context, client *http.Client, origin string) error {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	store, err := ReadStore(m.options.Directory)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	address, err := url.JoinPath(origin, "client", "compatibility")
	if err != nil {
		return err
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, address, nil)
	if err != nil {
		return err
	}
	if client == nil {
		client = http.DefaultClient
	}
	response, err := client.Do(request)
	if err != nil {
		m.clearServerRequirement()
		return nil // the resource check reports unreachable servers
	}
	defer func() { _ = response.Body.Close() }()
	if response.StatusCode != http.StatusOK {
		m.clearServerRequirement()
		return nil
	}
	var compat Compatibility
	if err := json.NewDecoder(io.LimitReader(response.Body, 16<<10)).Decode(&compat); err != nil {
		m.clearServerRequirement()
		return nil
	}
	if compat.LoginsClosed {
		return errors.New("The server is updating; try again after it finishes") //nolint:staticcheck // ST1005: shown to players
	}
	if compat.Fingerprint != nil {
		m.mu.Lock()
		known := m.fingerprint != nil && m.fingerprint.ID == compat.Fingerprint.ID
		m.mu.Unlock()
		if !known {
			if err := m.fetchServerFingerprint(ctx, client, origin, compat.Fingerprint.ID); err != nil {
				return err
			}
		}
	} else {
		m.clearServerRequirement()
	}
	versions := m.options.Versions
	m.mu.Lock()
	fp := m.fingerprint
	m.mu.Unlock()
	if fp != nil && fp.RequireClientUpdate && clientUpdateRequired(versions.ClientVersion, fp.ClientVersion) {
		return fmt.Errorf("update the launcher to %s before launching", fp.ClientVersion)
	}
	if fp != nil && fp.RequireClientUpdate {
		compat.MinimumPlugins = fp.MinimumPlugins
	}
	if compat.GameVersion != "" && versions.GameVersion != "" && compat.GameVersion != versions.GameVersion && (fp == nil || fp.Game == nil || fp.Game.GameVersion != compat.GameVersion) {
		return fmt.Errorf("Server game version %s does not match client game version %s", compat.GameVersion, versions.GameVersion) //nolint:staticcheck // ST1005: shown to players
	}
	installed := map[string]string{}
	for _, f := range store.Files {
		if f.ID != "" {
			installed[f.ID] = f.Version
		}
	}
	var stale []string
	for id, minimum := range compat.MinimumPlugins {
		current := installed[id]
		a, aErr := release.ParseVersion(current)
		b, bErr := release.ParseVersion(minimum)
		if aErr != nil || bErr != nil || a.Kind != b.Kind || a.HasGame != b.HasGame || a.Compare(b) < 0 {
			stale = append(stale, fmt.Sprintf("%s %s (needs %s)", id, current, minimum))
		}
	}
	slices.Sort(stale)
	if len(stale) != 0 {
		return fmt.Errorf("Server %s no longer accepts these plugins: %s. Update the plugins in the launcher first", compat.ServerVersion, strings.Join(stale, ", ")) //nolint:staticcheck // ST1005: shown to players
	}
	return nil
}

// WithLaunch keeps updates excluded until the prepared game process has started.
func (m *Manager) WithLaunch(ctx context.Context, client *http.Client, game, origin string, launch func() error) error {
	if !m.begin("preparing") {
		return operationBusy()
	}
	defer m.end()
	if err := m.CheckServer(ctx, client, origin); err != nil {
		return err
	}
	if err := m.ensureGame(ctx, game); err != nil {
		return err
	}
	if running, err := m.gameRunning(game); err != nil {
		return err
	} else if !running {
		if err := m.ensurePlugins(game); err != nil {
			return err
		}
	}
	return launch()
}
