package static

import (
	"bytes"
	"context"
	"net/url"
	"reflect"
	"strings"
	"testing"
)

func FuzzManifestCanonicalRoundTrip(f *testing.F) {
	hash := strings.Repeat("a", 64)
	f.Add([]byte(`{"version":1,"assets":[]}`))
	f.Add([]byte(`{"version":1,"assets":[{"path":"css/app.css","versioned":"css/` + hash + `.css","sha256":"` + hash + `","size":5}]}`))
	f.Add([]byte(`{"version":1,"version":2,"assets":[]}`))
	f.Add([]byte(`{"version":1,"assets":null}`))
	f.Add([]byte(`{"version":1,"assets":[]} {"version":2}`))
	f.Fuzz(func(t *testing.T, raw []byte) {
		if len(raw) > 16<<10 {
			t.Skip()
		}
		const prefix = "https://cdn.example.test/static/"
		ctx := context.Background()
		manifest, err := ReadManifest(ctx, bytes.NewReader(raw), prefix)
		if err != nil {
			if manifest != nil {
				t.Fatal("rejected manifest exposed a partial lookup")
			}
			return
		}
		wire := manifest.JSON()
		assets := manifest.Assets()
		again, err := ReadManifest(ctx, bytes.NewReader(wire), prefix)
		if err != nil || !bytes.Equal(again.JSON(), wire) || !reflect.DeepEqual(again.Assets(), assets) {
			t.Fatalf("accepted manifest failed canonical round trip: %v", err)
		}
		previous := ""
		for _, asset := range assets {
			if asset.Path <= previous || !validPath(asset.Path) || !validPath(asset.Versioned) || !validDigest(asset.SHA256) {
				t.Fatal("accepted manifest contains unordered or unsafe asset metadata")
			}
			previous = asset.Path
			address, err := manifest.URL(asset.Path)
			parsed, parseErr := url.Parse(address)
			if err != nil || parseErr != nil || parsed.Scheme != "https" || parsed.Host != "cdn.example.test" || parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" || parsed.Path != "/static/"+asset.Versioned {
				t.Fatalf("asset escaped configured URL namespace: %q, %v", address, err)
			}
			if repeat, err := again.URL(asset.Path); err != nil || repeat != address {
				t.Fatal("canonical reload changed asset URL")
			}
		}
		// Neither of the public inspection methods may expose manifest backing
		// memory to a deployment callback or template caller.
		if len(wire) > 0 {
			wire[0] = '!'
		}
		if len(assets) > 0 {
			assets[0].Versioned = "../private"
		}
		if !bytes.Equal(manifest.JSON(), again.JSON()) || !reflect.DeepEqual(manifest.Assets(), again.Assets()) {
			t.Fatal("public metadata mutation changed frozen manifest")
		}
	})
}
