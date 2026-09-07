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
	// (opengl32.dll shipped next to the EXE) is used, and its default
	// llvmpipe backend can abort the whole process (exit 0x80070057) while
	// Fyne uploads textures. Force the simpler softpipe backend instead: it
	// is pure software and stable. Real GPU drivers (NVIDIA/AMD/Intel) do
	// not read this Mesa-specific variable, so hardware rendering is
	// unaffected on normal machines.
	_ = os.Setenv("GALLIUM_DRIVER", "softpipe")

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
