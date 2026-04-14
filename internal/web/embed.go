package web

import "embed"

//go:embed static/*
var StaticFS embed.FS

func FS() embed.FS {
	return StaticFS
}
