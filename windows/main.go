package main

import (
	"embed"
	"log"
	"os"
	"strings"

	"github.com/wailsapp/wails/v2"
	"github.com/wailsapp/wails/v2/pkg/options"
	"github.com/wailsapp/wails/v2/pkg/options/assetserver"
	wailswindows "github.com/wailsapp/wails/v2/pkg/options/windows"
	"github.com/wailsapp/wails/v2/pkg/runtime"
)

//go:embed frontend/dist/*
var assets embed.FS

var appVersion = "dev"
var expectedEngineSHA256 = ""

const updatePublicKeyBase64 = "2ADwJkrQ2XxjFo4bC3mjQkxjGBpsj3hfP7JTV0wdtek="
const singleInstanceID = "91c18d93-c5bc-4888-9fa8-90c2316b34d3"

func main() {
	app := NewApp()
	background := false
	for _, arg := range os.Args[1:] {
		if strings.EqualFold(arg, "--background") {
			background = true
		}
	}
	app.backgroundLaunch = background

	err := wails.Run(&options.App{
		Title:             "MPTCP Desk",
		Width:             1040,
		Height:            760,
		MinWidth:          900,
		MinHeight:         640,
		StartHidden:       background,
		HideWindowOnClose: true,
		BackgroundColour:  &options.RGBA{R: 246, G: 247, B: 249, A: 255},
		AssetServer: &assetserver.Options{
			Assets: assets,
		},
		OnStartup:  app.startup,
		OnDomReady: app.domReady,
		OnShutdown: app.shutdown,
		SingleInstanceLock: &options.SingleInstanceLock{
			UniqueId: singleInstanceID,
			OnSecondInstanceLaunch: func(_ options.SecondInstanceData) {
				if app.ctx != nil {
					runtime.WindowUnminimise(app.ctx)
					runtime.Show(app.ctx)
				}
			},
		},
		Windows: &wailswindows.Options{
			Theme: wailswindows.SystemDefault,
		},
		Bind: []interface{}{app},
	})
	if err != nil {
		log.Fatal(err)
	}
}
