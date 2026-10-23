// Package dbpatch applies officially signed data migrations before the native
// client can load and clean its encrypted ClientLocal database.
package dbpatch

import (
	"bd2server/internal/common/util"
	"bytes"
	"context"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"strings"
	"time"

	"bd2server/internal/common/clientpatch"
	"bd2server/internal/common/release"
)

type Options struct {
	GameDirectory string
	Origin        string
	GameVersion   string
	Client        *http.Client
	Logger        *slog.Logger
	Running       func() (bool, error)
}

// Run fixes only the known ClientLocal database; packages cannot choose paths
// or provide trusted keys. The signing private key never reaches the server.
func Run(ctx context.Context, options Options) (Result, error) {
	path, err := LocalDatabasePath(options.GameDirectory)
	if err != nil {
		return Result{}, err
	}
	return run(ctx, path, options, release.BuiltinChannel().TrustedKeys)
}

func run(ctx context.Context, path string, options Options, keys []release.TrustedKey) (Result, error) {
	origin := strings.TrimSuffix(options.Origin, "/")
	if err := clientpatch.ValidateOrigin(origin); err != nil {
		return Result{}, err
	}
	if err := stopped(options.Running); err != nil {
		return Result{}, err
	}
	if _, err := os.Lstat(path); errors.Is(err, os.ErrNotExist) {
		return Result{}, nil
	} else if err != nil {
		return Result{}, err
	}
	ctx, cancel := context.WithTimeout(ctx, 45*time.Second)
	defer cancel()
	private, err := clientpatch.GenerateRecipient()
	if err != nil {
		return Result{}, err
	}
	requestBody, err := json.Marshal(map[string]string{"recipient_key": base64.StdEncoding.EncodeToString(private.PublicKey().Bytes())})
	if err != nil {
		return Result{}, err
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, origin+"/client/patches", bytes.NewReader(requestBody))
	if err != nil {
		return Result{}, err
	}
	request.Header.Set("Content-Type", "application/json")
	client := http.Client{}
	if options.Client != nil {
		client = *options.Client
	}
	client.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	response, err := client.Do(request)
	if err != nil {
		return Result{}, fmt.Errorf("dbpatch: download migration package: %w", err)
	}
	defer func() { _ = response.Body.Close() }()
	if response.StatusCode == http.StatusNotFound {
		// A server without published migrations can be used until this database
		// has participated in its migration channel. Losing that channel later
		// must not silently launch an unverified database.
		if err := missingCatalog(ctx, path, origin); err != nil {
			return Result{}, err
		}
		return Result{}, nil
	}
	if response.StatusCode != http.StatusOK {
		return Result{}, fmt.Errorf("dbpatch: server refused the migration package: %w", util.ResponseError(response))
	}
	file, err := io.ReadAll(io.LimitReader(response.Body, clientpatch.MaxPlaintext+1025))
	if err != nil {
		return Result{}, err
	}
	signed, err := clientpatch.Open(file, private)
	if err != nil {
		return Result{}, err
	}
	plan, err := clientpatch.Verify(signed, keys)
	if err != nil {
		return Result{}, err
	}
	if plan.Origin != origin || plan.GameVersion != options.GameVersion {
		return Result{}, errors.New("dbpatch: package targets a different server or game version")
	}
	result, err := apply(ctx, path, plan, options.Running)
	if err != nil {
		return Result{}, err
	}
	if len(result.Applied) != 0 {
		logger := options.Logger
		if logger == nil {
			logger = slog.Default()
		}
		logger.Info("client database migrations applied", "revision", result.Revision, "patches", result.Applied, "backup", result.Backup)
	}
	return result, nil
}

func missingCatalog(ctx context.Context, path, origin string) error {
	raw, err := readDatabase(path)
	if err != nil {
		return err
	}
	db, conn, err := openDatabase(ctx, raw)
	if err != nil {
		return err
	}
	defer func() { _ = conn.Close(); _ = db.Close() }()
	var count int
	if err := conn.QueryRowContext(ctx, "SELECT count(*) FROM sqlite_schema WHERE name='_bd2_patch_catalog'").Scan(&count); err != nil {
		return err
	}
	if count == 0 {
		return nil
	}
	var revision string
	err = conn.QueryRowContext(ctx, "SELECT revision FROM _bd2_patch_catalog WHERE origin=?", origin).Scan(&revision)
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	return fmt.Errorf("dbpatch: server lost the migration catalog after revision %s", revision)
}
