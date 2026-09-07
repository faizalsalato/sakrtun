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
	// (opengl32.dll shipped next to the EXE) is used; its default llvmpipe
	// backend is fast (LLVM JIT). Set GALLIUM_DRIVER=softpipe in the
	// environment to opt into the slower pure-software backend instead. Real
	// GPU drivers ignore this Mesa-specific variable.

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
