package webui

import (
	"embed"
	"io/fs"
)

//go:embed all:dist
var bundled embed.FS

// 返回以构建目录为根的离线界面资源。
func Assets() fs.FS {
	assets, err := fs.Sub(bundled, "dist")
	if err != nil {
		panic(err)
	}
	return assets
}
