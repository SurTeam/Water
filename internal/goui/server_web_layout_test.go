package goui

import (
	"image"
	"testing"

	"github.com/SurTeam/Water/internal/goconfig"
	"golang.org/x/image/font/gofont/goregular"
)

func TestWebSettingsActionsDoNotOverlapStatus(t *testing.T) {
	for _, language := range []string{"en", "zh-Hans"} {
		for _, scale := range []float64{1, 1.5, 2} {
			for _, height := range []float64{400, 440} {
				for _, tls := range []bool{false, true} {
					cfg := goconfig.Default()
					cfg.Web.TLS = tls
					cfg.UI.Language = language
					c := NewWorkspaceClientWithConnection(nil, nil, cfg, "")
					c.openSettings()
					c.settings.group = 5
					c.server.webVisible = true
					c.server.webConfigVisible = true
					c.server.message = "Server operation completed"
					w := &EbitengineWindow{scale: scale, fonts: &nativeFonts{ui: fontSource(goregular.TTF)}}
					r := image.Rect(0, 0, w.dp(560), w.dp(height))
					footer := r.Max.Y - w.dp(72)
					w.drawServerContent(c, nil, r, w.dp(52), footer, true)
					status := image.Rect(w.dp(24), footer-w.dp(26), r.Max.X-w.dp(24), footer-w.dp(4))
					controls := map[string]image.Rectangle{}
					for _, hit := range c.hitRegions {
						controls[hit.Label] = hit.Rect
						if hit.Rect.Overlaps(status) {
							t.Fatalf("language=%s scale=%g height=%g: %s overlaps status: %v / %v", language, scale, height, hit.Label, hit.Rect, status)
						}
					}
					for _, action := range []string{"server:web-configure", "server:web-close"} {
						button, ok := controls[action]
						if !ok || button.Min.Y != controls["server:web-dismiss"].Min.Y || button.Max.Y != controls["server:web-dismiss"].Max.Y {
							t.Fatalf("%s must share the footer row with Close: %v", action, button)
						}
					}
					if _, ok := controls["server:restart"]; ok {
						t.Fatal("editing Web settings must use its own footer actions")
					}
					if _, ok := controls["field:Web.TLSCertFile"]; ok != tls {
						t.Fatal("HTTPS fields must follow the TLS switch")
					}
					if _, ok := controls["field:Web.PublicURL"]; ok {
						t.Fatal("obsolete URL control remains")
					}
				}
			}
		}
	}
}
