package desktop

import "quarry/internal/platform"

// loadStartupInfo runs finite background lookups once, after the first
// frame. Only posted callbacks change state read by the UI.
func (a *App) loadStartupInfo() {
	if a.startupStarted {
		return
	}
	a.startupStarted = true
	go func() {
		drives := platform.Drives()
		explorerVerb := platform.ExplorerVerbInstalled()
		a.post(func() {
			a.setDrives(drives)
			a.dlg.explorerVerb = explorerVerb
		})
	}()
	go func() {
		icons := loadShellIcons()
		a.post(func() { a.shellIcons = icons })
	}()
}

func (a *App) setDrives(drives []platform.Drive) {
	a.drives, a.drivesReady = drives, true
	if a.target == "" && len(drives) > 0 {
		a.target = systemDrive(drives)
	}
	if a.menu != nil {
		// This menu also owns the drive list; rebuild it on the next frame.
		a.menu.recentChanged = true
	}
}
