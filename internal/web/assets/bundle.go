package assets

import (
	"github.com/gosuda/rukie/internal/web/asset"
	"github.com/gosuda/rukie/internal/web/trusted"
)

func Bundle() asset.Asset {
	return asset.Asset{
		Name:        "app.js",
		ContentType: "application/javascript",
		Bytes:       mustRead("app.js"),
	}
}

func BundleURL() string {
	return "/-/app/app." + Bundle().ShortHash() + ".js"
}

func HydrateScript() trusted.HTML {
	return trusted.HTML(`<script type="module" src="` + BundleURL() + `"></script>`)
}
