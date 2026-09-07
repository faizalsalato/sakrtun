package main

import (
	"log"
	"os"
	"path/filepath"
	"runtime/debug"

	"socksrevivepc/internal/app"
	"socksrevivepc/internal/crash"
	"socksrevivepc/internal/nativeui"
)

func main() {
	debug.SetTraceback("all")
	// Some virtual machines and remote sessions have no hardware OpenGL
	// driver. On those machines the bundled Mesa software renderer
	// (opengl32.dll shipped next to the EXE) is used. Force its fast llvmpipe
	// backend: it is the reliable default for machines without a GPU, while
	// this Mesa build's own default (D3D12) aborts on GPU-less systems. Real
	// GPU drivers ignore this Mesa-specific variable; set GALLIUM_DRIVER to
	// override (for example softpipe).
	if os.Getenv("GALLIUM_DRIVER") == "" {
		_ = os.Setenv("GALLIUM_DRIVER", "llvmpipe")
	}

	root := "."
	if wd, err := os.Getwd(); err == nil && wd != "" {
		root = wd
	}
	// Remove leftovers from a previous self-update (the old executable is
	// locked while the app runs and can only be deleted after a restart).
	_ = os.Remove(filepath.Join(root, "SAKRTUN.exe.old"))
	closeLog := crash.AttachLog(root)
	defer closeLog()
	defer crash.Recover(root)

	application, err := app.New()
	if err != nil {
		crash.Write(root, "init failed", err)
		log.Printf("init failed: %v", err)
		return
	}
	log.Printf("app root: %s", application.Root)
	nativeui.Run(application)
}
