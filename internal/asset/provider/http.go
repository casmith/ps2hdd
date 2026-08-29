package provider

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/casmith/ps2hdd/internal/model"
)

// Doer is the subset of http.Client the providers use, so tests can substitute
// a transport without a network.
type Doer interface {
	Do(req *http.Request) (*http.Response, error)
}

// defaultClient has a timeout so a hung mirror cannot wedge a sync.
func defaultClient() Doer {
	return &http.Client{Timeout: 30 * time.Second}
}

// Template placeholders understood in a URL template:
//
//	{serial}        SLUS-20946   the dashed form the cover databases use
//	{serial_opl}    SLUS_209.46  the OPL form
//	{serial_plain}  SLUS20946    punctuation stripped
//	{type}          COV          the asset type suffix
//	{platform}      ps2 / ps1
const (
	phSerial      = "{serial}"
	phSerialOPL   = "{serial_opl}"
	phSerialPlain = "{serial_plain}"
	phType        = "{type}"
	phPlatform    = "{platform}"
)

// expandTemplate substitutes the placeholders for one game and asset type.
func expandTemplate(tmpl string, game model.Game, t model.AssetType) string {
	r := strings.NewReplacer(
		phSerial, model.DashedGameID(game.GameID),
		phSerialOPL, model.OPLGameID(game.GameID),
		phSerialPlain, model.NormalizeGameID(game.GameID),
		phType, string(t),
		phPlatform, string(game.Platform),
	)
	return r.Replace(tmpl)
}

// httpTemplate serves assets from URL templates, one per asset type.
type httpTemplate struct {
	name string
	// templates maps an asset type to a URL template. A type with no template
	// is one this provider cannot supply, which is reported honestly rather
	// than guessed at.
	templates map[model.AssetType]string
	// probe is a URL that Check requests to decide whether the source is up.
	probe  string
	client Doer
}

func newHTTPTemplate(o Options) (Provider, error) {
	if len(o.Templates) == 0 {
		return nil, fmt.Errorf("the http artwork provider needs [assets.templates] entries in the config file, e.g. COV = \"https://example/{serial}.png\"")
	}
	p := &httpTemplate{name: "http", templates: map[model.AssetType]string{}, client: o.HTTP}
	if p.client == nil {
		p.client = defaultClient()
	}
	for k, v := range o.Templates {
		p.templates[model.AssetType(strings.ToUpper(k))] = v
	}
	for _, v := range p.templates {
		p.probe = v
		break
	}
	return p, nil
}

func (p *httpTemplate) Name() string { return p.name }

func (p *httpTemplate) Lookup(ctx context.Context, game model.Game, want []model.AssetType) (model.AssetSet, error) {
	var set model.AssetSet
	if model.NormalizeGameID(game.GameID) == "" {
		return set, nil
	}
	for _, t := range want {
		tmpl, ok := p.templates[t]
		if !ok {
			continue
		}
		set.Assets = append(set.Assets, model.Asset{
			Type:     t,
			GameID:   game.GameID,
			Platform: game.Platform,
			Source:   expandTemplate(tmpl, game, t),
		})
	}
	return set, nil
}

func (p *httpTemplate) Fetch(ctx context.Context, a model.Asset) (io.ReadCloser, error) {
	return httpFetch(ctx, p.client, a.Source)
}

func (p *httpTemplate) Check(ctx context.Context) error {
	if p.probe == "" {
		return fmt.Errorf("%s: no URL templates configured", p.name)
	}
	return httpReachable(ctx, p.client, hostOf(p.probe))
}

// ps2covers serves cover art from the xlenore PS2 and PS1 cover collections,
// which are the databases PCSX2 and DuckStation ship as their default cover
// sources and which are reachable over plain HTTPS with no authentication.
//
// They hold front covers only. Every other OPL art slot is reported as
// unavailable rather than filled with a substitute, because a background made
// from a cover looks wrong on a PS2 and a user would rather see "missing".
type ps2covers struct {
	client Doer
}

// The templates below use the dashed serial form these repositories index by.
const (
	ps2CoverTemplate = "https://raw.githubusercontent.com/xlenore/ps2-covers/main/covers/default/{serial}.jpg"
	ps1CoverTemplate = "https://raw.githubusercontent.com/xlenore/psx-covers/main/covers/default/{serial}.jpg"
)

func newPS2Covers(o Options) (Provider, error) {
	c := o.HTTP
	if c == nil {
		c = defaultClient()
	}
	return &ps2covers{client: c}, nil
}

func (p *ps2covers) Name() string { return "ps2-covers" }

func (p *ps2covers) Lookup(ctx context.Context, game model.Game, want []model.AssetType) (model.AssetSet, error) {
	var set model.AssetSet
	if model.NormalizeGameID(game.GameID) == "" {
		return set, nil
	}
	tmpl := ps2CoverTemplate
	if game.Platform == model.PlatformPS1 {
		tmpl = ps1CoverTemplate
	}
	for _, t := range want {
		if t != model.AssetCover {
			continue
		}
		set.Assets = append(set.Assets, model.Asset{
			Type:     t,
			GameID:   game.GameID,
			Platform: game.Platform,
			Source:   expandTemplate(tmpl, game, t),
		})
	}
	return set, nil
}

func (p *ps2covers) Fetch(ctx context.Context, a model.Asset) (io.ReadCloser, error) {
	return httpFetch(ctx, p.client, a.Source)
}

func (p *ps2covers) Check(ctx context.Context) error {
	return httpReachable(ctx, p.client, "https://raw.githubusercontent.com/")
}

// ErrNotAvailable means the provider has no such asset. It is distinct from a
// transport failure: one means "this game has no cover", the other means "the
// database is unreachable", and the two need different messages.
var ErrNotAvailable = fmt.Errorf("asset not available from this provider")

func httpFetch(ctx context.Context, c Doer, url string) (io.ReadCloser, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", "ps2hdd")
	resp, err := c.Do(req)
	if err != nil {
		return nil, fmt.Errorf("fetch %s: %w", url, err)
	}
	if resp.StatusCode == http.StatusNotFound {
		resp.Body.Close()
		return nil, fmt.Errorf("%w: %s", ErrNotAvailable, url)
	}
	if resp.StatusCode != http.StatusOK {
		resp.Body.Close()
		return nil, fmt.Errorf("fetch %s: HTTP %s", url, resp.Status)
	}
	return resp.Body, nil
}

func httpReachable(ctx context.Context, c Doer, url string) error {
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodHead, url, nil)
	if err != nil {
		return err
	}
	req.Header.Set("User-Agent", "ps2hdd")
	resp, err := c.Do(req)
	if err != nil {
		return fmt.Errorf("artwork provider unreachable: %w", err)
	}
	defer resp.Body.Close()
	// Any answer at all proves the host is up; a 404 on the probe path is fine.
	if resp.StatusCode >= 500 {
		return fmt.Errorf("artwork provider returned HTTP %s", resp.Status)
	}
	return nil
}

func hostOf(rawURL string) string {
	i := strings.Index(rawURL, "://")
	if i < 0 {
		return rawURL
	}
	rest := rawURL[i+3:]
	if j := strings.IndexByte(rest, '/'); j >= 0 {
		return rawURL[:i+3] + rest[:j] + "/"
	}
	return rawURL
}
