//go:build !windows

package platform

import "image"

func StockIcons() map[string]image.Image { return nil }
