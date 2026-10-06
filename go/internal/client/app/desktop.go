package app

import (
	"context"
	"fmt"
	"io/fs"
	"net/http"

	"github.com/wailsapp/wails/v2"
	"github.com/wailsapp/wails/v2/pkg/options"
	"github.com/wailsapp/wails/v2/pkg/options/assetserver"
	"github.com/wailsapp/wails/v2/pkg/options/mac"
	"github.com/wailsapp/wails/v2/pkg/options/windows"
	wailsruntime "github.com/wailsapp/wails/v2/pkg/runtime"
)

// Run starts one native desktop window. Wails uses WebView2 on Windows and
// WKWebView on macOS; no loopback listener or external browser is involved.
func Run(runOptions Options) error {
	assets, err := fs.Sub(webFS, "web")
	if err != nil {
		return fmt.Errorf("load embedded client interface: %w", err)
	}
	host := NativeHost{
		BrowseDirectory: func(ctx context.Context, title string) (string, error) {
			return wailsruntime.OpenDirectoryDialog(ctx, wailsruntime.OpenDialogOptions{Title: title})
		},
		Quit: wailsruntime.Quit,
	}
	studio := NewStudio(runOptions, host)
	studio.log().Info("starting native client window", "windows_engine", "WebView2", "macos_engine", "WKWebView")
	err = wails.Run(&options.App{
		Title:            "BD2 Client Studio",
		Width:            1080,
		Height:           720,
		MinWidth:         860,
		MinHeight:        580,
		BackgroundColour: options.NewRGB(231, 220, 199),
		AssetServer: &assetserver.Options{
			Assets:     assets,
			Middleware: desktopSecurityHeaders,
		},
		OnStartup: studio.Startup,
		OnShutdown: func(context.Context) {
			studio.Shutdown()
			studio.log().Info("native client window closed")
		},
		Bind:                     []any{studio},
		EnableDefaultContextMenu: false,
		DragAndDrop: &options.DragAndDrop{
			DisableWebViewDrop: true,
		},
		Windows: &windows.Options{
			Theme:                windows.Light,
			BackdropType:         windows.Mica,
			DisablePinchZoom:     true,
			IsZoomControlEnabled: false,
			EnableSwipeGestures:  false,
		},
		Mac: &mac.Options{
			TitleBar:    mac.TitleBarDefault(),
			Appearance:  mac.NSAppearanceNameAqua,
			DisableZoom: true,
		},
	})
	if err != nil {
		studio.log().Error("native client window stopped with an error", "error", err)
		return fmt.Errorf("run native client window: %w", err)
	}
	return nil
}

func desktopSecurityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Referrer-Policy", "no-referrer")
		w.Header().Set("X-Frame-Options", "DENY")
		w.Header().Set("Content-Security-Policy", "default-src 'self'; style-src 'self' 'unsafe-inline'; script-src 'self' 'unsafe-inline'; img-src 'self' data:; connect-src 'self'")
		next.ServeHTTP(w, r)
	})
}
